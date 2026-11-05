package proxy

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/acpboss/internal/config"
	"github.com/baldaworks/acpboss/internal/provider"
	acp "github.com/coder/acp-go-sdk"
)

var (
	ErrCapacity     = errors.New("proxy capacity exhausted")
	ErrUnknownModel = errors.New("unknown or unavailable model")
	ErrBusy         = errors.New("session operation already active")
	ErrInteraction  = errors.New("interactive permission requests are unsupported over HTTP")
	ErrBackpressure = errors.New("session update queue exceeded limit")
)

// Permission routes a permission request to the owning ACP client.
type Permission func(context.Context, map[string]any) (any, error)
type route struct{ provider, session string }
type binding struct {
	unusable bool
	id       string
	process  *provider.Process
	metadata *provider.Metadata
	session  *session
}
type operation struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	updates chan map[string]any
	backend *binding
}
type session struct {
	id, selected string
	bindings     map[string]*binding
	permission   Permission
	busy         bool
	active       *operation
}

// Router owns logical sessions. Its mutex is never held across upstream I/O.
type Router struct {
	Config    config.Config
	startMu   sync.Mutex
	mu        sync.Mutex
	manager   *provider.Manager
	sessions  map[string]*session
	routes    map[route]*binding
	catalog   map[string]*provider.Metadata
	semaphore chan struct{}
	closed    bool
}

// New constructs an unstarted router; ACP initializes it on frontend initialize.
func New(c config.Config) *Router {
	return &Router{Config: c, sessions: map[string]*session{}, routes: map[route]*binding{}, catalog: map[string]*provider.Metadata{}, semaphore: make(chan struct{}, c.Settings.MaxInFlight)}
}

// Start atomically starts all configured provider processes once.
func (r *Router) Start(ctx context.Context) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	r.mu.Lock()
	closed := r.closed
	started := r.manager != nil
	r.mu.Unlock()
	if closed {
		return fmt.Errorf("proxy closed")
	}
	if started {
		return nil
	}
	m, err := provider.StartAll(ctx, r.Config, r.handle)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.manager = m
	r.mu.Unlock()
	return nil
}

func (r *Router) processes() []*provider.Process {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.manager == nil {
		return nil
	}
	return r.manager.Processes
}

// Close cancels work and reaps all children under the global shutdown budget.
func (r *Router) Close() error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	r.mu.Lock()
	r.closed = true
	for _, s := range r.sessions {
		if s.active != nil {
			s.active.cancel(context.Canceled)
		}
	}
	m := r.manager
	r.sessions = map[string]*session{}
	r.routes = map[route]*binding{}
	r.mu.Unlock()
	if m != nil {
		return m.Close()
	}
	return nil
}

// Discover obtains the HTTP model catalog in bounded temporary sessions.
func (r *Router) Discover(ctx context.Context) error {
	id, err := r.Create(ctx, r.Config.Settings.WorkingDir, nil, nil, "")
	if err != nil {
		return err
	}
	r.mu.Lock()
	current := r.sessions[id]
	if current == nil {
		r.mu.Unlock()
		return context.Canceled
	}
	for pid, b := range current.bindings {
		r.catalog[pid] = b.metadata
	}
	r.mu.Unlock()
	return r.Release(ctx, id)
}

// Catalog returns a sorted snapshot excluding dead provider connections.
func (r *Router) Catalog() []Model {
	r.mu.Lock()
	defer r.mu.Unlock()
	var models []Model
	if r.manager != nil {
		for _, p := range r.manager.Processes {
			if m := r.catalog[p.Config.ID]; m != nil {
				models = append(models, AvailableModels(p, m)...)
			}
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

// Create makes a logical session across live providers, or one provider for HTTP.
func (r *Router) Create(ctx context.Context, cwd string, mcp []any, permission Permission, only string) (string, error) {
	if !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("session cwd must be absolute")
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("session cwd must exist")
	}
	s := &session{id: "session-" + rand.Text(), bindings: map[string]*binding{}, permission: permission, busy: true}
	r.mu.Lock()
	if r.closed || r.manager == nil {
		r.mu.Unlock()
		return "", fmt.Errorf("proxy is not initialized")
	}
	if len(r.sessions) >= r.Config.Settings.MaxSessions {
		r.mu.Unlock()
		return "", ErrCapacity
	}
	r.sessions[s.id] = s
	r.mu.Unlock()
	ok := false
	defer func() {
		if !ok {
			cleanup, c := context.WithTimeout(context.Background(), r.Config.Settings.ShutdownTimeout)
			defer c()
			_ = r.Release(cleanup, s.id)
		}
	}()
	for _, p := range r.processes() {
		if !p.Alive() || (only != "" && p.Config.ID != only) {
			continue
		}
		meta, err := p.NewSession(ctx, cwd, mcp)
		if err != nil {
			return "", err
		}
		b := &binding{id: meta.ID, process: p, metadata: meta, session: s}
		r.mu.Lock()
		key := route{p.Config.ID, meta.ID}
		if r.closed || r.sessions[s.id] != s {
			r.mu.Unlock()
			return "", context.Canceled
		}
		if r.routes[key] != nil {
			r.mu.Unlock()
			return "", fmt.Errorf("provider reused an active session ID")
		}
		s.bindings[p.Config.ID] = b
		r.routes[key] = b
		if s.selected == "" {
			s.selected = p.Config.ID
		}
		r.mu.Unlock()
		if p.Config.Defaults.Model != "" {
			meta, err = p.SelectModel(ctx, meta, p.Config.Defaults.Model)
			if err != nil {
				return "", err
			}
		}
		for _, setting := range []struct{ value, id, category string }{{p.Config.Defaults.Mode, "", "mode"}, {p.Config.Defaults.ReasoningEffort, p.Config.Defaults.ReasoningEffortConfigID, "thought_level"}} {
			if setting.value == "" {
				continue
			}
			id := setting.id
			options, _ := meta.Raw["configOptions"].([]any)
			for _, v := range options {
				o, _ := v.(map[string]any)
				if id == "" && o["category"] == setting.category {
					id, _ = o["id"].(string)
				}
			}
			var control json.RawMessage
			if id == "" && setting.category == "mode" {
				control, err = p.Call(ctx, "session/set_mode", map[string]any{"sessionId": meta.ID, "modeId": setting.value})
			} else if id != "" {
				control, err = p.Call(ctx, "session/set_config_option", map[string]any{"sessionId": meta.ID, "configId": id, "value": setting.value})
			} else {
				err = fmt.Errorf("provider does not advertise configured %s", setting.category)
			}
			if err != nil {
				return "", err
			}
			meta, err = refreshMetadata(meta, control)
			if err != nil {
				return "", err
			}
			if id == "" && setting.category == "mode" {
				if modes, ok := meta.Raw["modes"].(map[string]any); ok {
					modes["currentModeId"] = setting.value
				}
			}
		}
		r.mu.Lock()
		b.metadata = meta
		r.mu.Unlock()
	}
	r.mu.Lock()
	s.busy = false
	hasBindings := len(s.bindings) > 0
	r.mu.Unlock()
	if !hasBindings {
		return "", ErrUnknownModel
	}
	ok = true
	return s.id, nil
}

// PrepareHTTP validates a catalog model and creates an isolated selected conversation.
func (r *Router) PrepareHTTP(ctx context.Context, model string) (string, error) {
	pid, _, err := SplitModel(model)
	if err != nil {
		return "", ErrUnknownModel
	}
	found := false
	for _, m := range r.Catalog() {
		if m.ID == model {
			found = true
			break
		}
	}
	if !found {
		return "", ErrUnknownModel
	}
	id, err := r.Create(ctx, r.Config.Settings.WorkingDir, nil, nil, pid)
	if err != nil {
		return "", err
	}
	if err := r.Select(ctx, id, model); err != nil {
		_ = r.Release(ctx, id)
		return "", err
	}
	return id, nil
}

// Select changes model/provider only when the session has no active operation.
func (r *Router) Select(ctx context.Context, id, model string) error {
	pid, up, err := SplitModel(model)
	if err != nil {
		return ErrUnknownModel
	}
	r.mu.Lock()
	s := r.sessions[id]
	if s == nil {
		r.mu.Unlock()
		return fmt.Errorf("unknown session")
	}
	if s.busy {
		r.mu.Unlock()
		return ErrBusy
	}
	b := s.bindings[pid]
	if b == nil || b.unusable || !b.process.Alive() {
		r.mu.Unlock()
		return ErrUnknownModel
	}
	s.busy = true
	meta := b.metadata
	r.mu.Unlock()
	next, err := b.process.SelectModel(ctx, meta, up)
	r.mu.Lock()
	defer r.mu.Unlock()
	s.busy = false
	if err != nil {
		return err
	}
	b.metadata = next
	s.selected = pid
	return nil
}

// Snapshot returns a detached aggregate selector and selected provider's controls.
func (r *Router) Snapshot(id string) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	if s == nil {
		return nil, fmt.Errorf("unknown session")
	}
	return r.snapshotLocked(s), nil
}
func (r *Router) snapshotLocked(s *session) map[string]any {
	choices := []any{}
	ids := make([]string, 0, len(s.bindings))
	for id := range s.bindings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b := s.bindings[id]
		for _, m := range AvailableModels(b.process, b.metadata) {
			choices = append(choices, map[string]any{"value": m.ID, "name": id + " / " + m.Name})
		}
	}
	current := ""
	var options []any
	var modes any
	if b := s.bindings[s.selected]; b != nil {
		current = s.selected + "/" + b.metadata.Current
		options, _ = b.metadata.Raw["configOptions"].([]any)
		modes = b.metadata.Raw["modes"]
	}
	result := []any{map[string]any{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": current, "options": choices}}
	for _, raw := range options {
		o, _ := raw.(map[string]any)
		if o["category"] == "model" || o["id"] == "model" {
			continue
		}
		b := s.bindings[s.selected]
		if o["id"] == b.metadata.ModelConfigID {
			continue
		}
		result = append(result, o)
	}
	out := map[string]any{"sessionId": s.id, "configOptions": result}
	if modes != nil {
		out["modes"] = modes
	}
	return clone(out)
}

// SetControl forwards a selected-provider non-model config/mode operation.
func (r *Router) SetControl(ctx context.Context, id, method string, params map[string]any) (map[string]any, error) {
	r.mu.Lock()
	s := r.sessions[id]
	if s == nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("unknown session")
	}
	if s.busy {
		r.mu.Unlock()
		return nil, ErrBusy
	}
	b := s.bindings[s.selected]
	s.busy = true
	r.mu.Unlock()
	p := clone(params)
	p["sessionId"] = b.id
	raw, err := b.process.Call(ctx, method, p)
	r.mu.Lock()
	defer r.mu.Unlock()
	s.busy = false
	if err != nil {
		return nil, err
	}
	next, err := refreshMetadata(b.metadata, raw)
	if err != nil {
		return nil, err
	}
	b.metadata = next
	if method == "session/set_mode" {
		if modes, ok := b.metadata.Raw["modes"].(map[string]any); ok {
			modes["currentModeId"] = params["modeId"]
		}
	}
	return r.snapshotLocked(s), nil
}

// Prompt forwards updates through a bounded per-session queue and drains before return.
func (r *Router) Prompt(ctx context.Context, id string, blocks []any, sink func(map[string]any) error) (map[string]any, error) {
	retained := false
	select {
	case r.semaphore <- struct{}{}:
		defer func() {
			if !retained {
				<-r.semaphore
			}
		}()
	default:
		return nil, ErrCapacity
	}
	deadline, dc := context.WithTimeout(ctx, r.Config.Settings.RequestTimeout)
	defer dc()
	opCtx, cancel := context.WithCancelCause(deadline)
	defer cancel(context.Canceled)
	op := &operation{ctx: opCtx, cancel: cancel, updates: make(chan map[string]any, 64)}
	r.mu.Lock()
	s := r.sessions[id]
	if s == nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("unknown session")
	}
	if s.busy {
		r.mu.Unlock()
		return nil, ErrBusy
	}
	b := s.bindings[s.selected]
	if b.unusable || !b.process.Alive() {
		r.mu.Unlock()
		return nil, ErrUnknownModel
	}
	if err := r.validateContent(b, blocks); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	s.busy = true
	op.backend = b
	s.active = op
	sid := b.id
	r.mu.Unlock()
	retained = true
	finished := make(chan struct{})
	watcherDone := make(chan struct{})
	sent := make(chan struct{})
	rpcCtx, rpcCancel := context.WithTimeout(context.WithoutCancel(ctx), r.Config.Settings.RequestTimeout+r.Config.Settings.ShutdownTimeout)
	defer func() {
		cancel(context.Canceled)
		cleanup := func() {
			<-finished
			<-watcherDone
			rpcCancel()
			r.mu.Lock()
			if s.active == op {
				s.busy = false
				s.active = nil
			}
			r.mu.Unlock()
			<-r.semaphore
		}
		select {
		case <-finished:
			cleanup()
		default:
			go cleanup()
		}
	}()
	if instruction := b.process.Config.Instructions; instruction != "" {
		blocks = append([]any{map[string]any{"type": "text", "text": instruction}}, blocks...)
	}
	type answer struct {
		raw json.RawMessage
		err error
	}
	done := make(chan answer, 1)
	go func() {
		raw, err := b.process.Prompt(rpcCtx, sid, blocks, sent)
		if errors.Is(err, context.DeadlineExceeded) {
			r.mu.Lock()
			b.unusable = true
			r.mu.Unlock()
		}
		close(finished)
		done <- answer{raw, err}
	}()
	go func() {
		defer close(watcherDone)
		select {
		case <-finished:
			return
		case <-opCtx.Done():
		}
		select {
		case <-finished:
			return
		default:
		}
		select {
		case <-finished:
			return
		case <-sent:
		}
		select {
		case <-finished:
			return
		default:
		}
		notifyCtx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = b.process.Notify(notifyCtx, "session/cancel", map[string]any{"sessionId": sid})
	}()
	deliver := func(v map[string]any) error {
		if sink == nil {
			return nil
		}
		return sink(v)
	}
	for {
		select {
		case <-opCtx.Done():
			return nil, context.Cause(opCtx)
		case v := <-op.updates:
			if err := deliver(v); err != nil {
				cancel(err)
				return nil, err
			}
		case result := <-done:
			for {
				select {
				case v := <-op.updates:
					if err := deliver(v); err != nil {
						cancel(err)
						return nil, err
					}
				default:
					goto drained
				}
			}
		drained:
			if cause := context.Cause(opCtx); cause != nil {
				return nil, cause
			}
			if result.err != nil {
				return nil, result.err
			}
			var out map[string]any
			if json.Unmarshal(result.raw, &out) != nil {
				return nil, fmt.Errorf("invalid prompt result")
			}
			return out, nil
		}
	}
}

// Cancel cancels the captured active provider, independently of later selection.
func (r *Router) Cancel(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[id]; s != nil && s.active != nil {
		s.active.cancel(context.Canceled)
	}
}

// Release removes logical routes and requests backend cleanup when supported.
func (r *Router) Release(ctx context.Context, id string) error {
	r.mu.Lock()
	s := r.sessions[id]
	if s == nil {
		r.mu.Unlock()
		return nil
	}
	delete(r.sessions, id)
	if s.active != nil {
		s.active.cancel(context.Canceled)
	}
	bindings := make([]*binding, 0, len(s.bindings))
	for pid, b := range s.bindings {
		delete(r.routes, route{pid, b.id})
		bindings = append(bindings, b)
	}
	r.mu.Unlock()
	for _, b := range bindings {
		if b.process.Init.AgentCapabilities.SessionCapabilities.Close != nil && b.process.Alive() {
			_, _ = b.process.Call(ctx, "session/close", map[string]any{"sessionId": b.id})
		}
	}
	return nil
}

// AuthenticationMethods qualifies authentication IDs by provider.
func (r *Router) AuthenticationMethods() []any {
	var all []any
	for _, p := range r.processes() {
		raw, _ := json.Marshal(p.Init.AuthMethods)
		var list []map[string]any
		_ = json.Unmarshal(raw, &list)
		for _, m := range list {
			if id, ok := m["id"].(string); ok {
				m["id"] = p.Config.ID + "/" + id
				all = append(all, m)
			}
		}
	}
	return all
}

// Authenticate sends one namespaced authentication method only to its owner.
func (r *Router) Authenticate(ctx context.Context, id string) error {
	pid, method, err := SplitModel(id)
	if err != nil {
		return err
	}
	for _, p := range r.processes() {
		if p.Config.ID == pid {
			_, err := p.Call(ctx, "authenticate", map[string]any{"methodId": method})
			return err
		}
	}
	return fmt.Errorf("unknown authentication provider")
}

func (r *Router) handle(ctx context.Context, pid, method string, raw json.RawMessage) (any, error) {
	var params map[string]any
	if json.Unmarshal(raw, &params) != nil {
		return nil, acp.NewInvalidParams(nil)
	}
	sid, _ := params["sessionId"].(string)
	r.mu.Lock()
	b := r.routes[route{pid, sid}]
	if b == nil {
		r.mu.Unlock()
		if method == "session/update" {
			return nil, nil
		}
		return nil, acp.NewInvalidParams(nil)
	}
	s := b.session
	op := s.active
	if op != nil && op.backend != b {
		op = nil
	}
	if method == "session/update" {
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] == "current_model_update" {
			for _, key := range []string{"modelId", "currentModelId"} {
				if model, ok := update[key].(string); ok {
					copy := *b.metadata
					copy.Current = model
					b.metadata = &copy
					update[key] = pid + "/" + model
				}
			}
		}
		if tool, ok := update["toolCallId"].(string); ok {
			update["toolCallId"] = pid + "/" + tool
		}
		if opts, ok := update["configOptions"]; ok {
			obj := clone(b.metadata.Raw)
			obj["configOptions"] = opts
			encoded, _ := json.Marshal(obj)
			if m, err := provider.DecodeMetadata(encoded, b.metadata.ModelConfigID); err == nil {
				b.metadata = m
			}
			if s.selected == pid {
				params = map[string]any{"sessionId": s.id, "update": map[string]any{"sessionUpdate": "config_option_update", "configOptions": r.snapshotLocked(s)["configOptions"]}}
			}
		}
		params["sessionId"] = s.id
		if op != nil {
			select {
			case op.updates <- params:
			default:
				op.cancel(ErrBackpressure)
			}
		}
		r.mu.Unlock()
		return nil, nil
	}
	if tool, ok := params["toolCall"].(map[string]any); ok {
		if id, ok := tool["toolCallId"].(string); ok {
			tool["toolCallId"] = pid + "/" + id
		}
	}
	permission := s.permission
	params["sessionId"] = s.id
	r.mu.Unlock()
	if method != "session/request_permission" {
		return nil, acp.NewMethodNotFound(method)
	}
	if op == nil {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
	if permission == nil {
		op.cancel(ErrInteraction)
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
	return permission(op.ctx, params)
}

func clone(in map[string]any) map[string]any {
	b, _ := json.Marshal(in)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// Text extracts only visible ACP assistant text from one notification.
func Text(note map[string]any) string {
	update, _ := note["update"].(map[string]any)
	if update["sessionUpdate"] != "agent_message_chunk" {
		return ""
	}
	content, _ := update["content"].(map[string]any)
	if content["type"] != "text" {
		return ""
	}
	text, _ := content["text"].(string)
	return text
}

// TextPrompt constructs the ACP text block for a normalized HTTP prompt.
func TextPrompt(text string) []any { return []any{map[string]any{"type": "text", "text": text}} }

// IsSetupTimeout identifies expired setup/initialization operations.
func IsSetupTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline exceeded")
}

func refreshMetadata(m *provider.Metadata, raw json.RawMessage) (*provider.Metadata, error) {
	var changes map[string]any
	if json.Unmarshal(raw, &changes) != nil {
		return nil, fmt.Errorf("invalid control response")
	}
	obj := clone(m.Raw)
	for k, v := range changes {
		obj[k] = v
	}
	obj["sessionId"] = m.ID
	encoded, _ := json.Marshal(obj)
	return provider.DecodeMetadata(encoded, m.ModelConfigID)
}
