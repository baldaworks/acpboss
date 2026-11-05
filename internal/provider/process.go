// Package provider owns ACP subprocesses and their SDK connections.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/baldaworks/acpboss/internal/config"
	"github.com/baldaworks/acpboss/internal/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// Handler dispatches a provider callback, retaining the provider connection identity.
type Handler func(context.Context, string, string, json.RawMessage) (any, error)

// Process owns one ACP process and one SDK connection.
type Process struct {
	Config config.Resolved
	Init   acp.InitializeResponse
	rpc    *acp.Connection
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cancel context.CancelFunc
	done   chan struct{}
	closed chan struct{}
	once   sync.Once
	mu     sync.Mutex
	err    error
	sent   map[string]chan struct{}
}

// Start launches a prevalidated command and initializes ACP with a separate timeout.
func Start(ctx context.Context, c config.Resolved, s config.Settings, handler Handler) (*Process, error) {
	procCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(procCtx, c.Command[0], c.Command[1:]...)
	cmd.Env = c.Environment
	cmd.Dir = s.WorkingDir
	cmd.Stderr = io.Discard
	configureProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("provider %q: launch failed", c.ID)
	}
	p := &Process{Config: c, cmd: cmd, stdin: stdin, stdout: stdout, cancel: cancel, done: make(chan struct{}), closed: make(chan struct{}), sent: map[string]chan struct{}{}}
	var release func()
	p.rpc, release = protocol.Open(func(ctx context.Context, method string, raw json.RawMessage) (any, *acp.RequestError) {
		if handler == nil {
			return nil, acp.NewMethodNotFound(method)
		}
		value, err := handler(ctx, c.ID, method, raw)
		if err != nil {
			var re *acp.RequestError
			if errors.As(err, &re) {
				return nil, re
			}
			return nil, acp.NewInternalError(nil)
		}
		return value, nil
	}, &observedWriter{file: stdin.(*os.File), observe: p.observe}, stdout, s.MaxMessageBytes, s.RequestTimeout)
	release()
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.done)
		killProcess(cmd)
		_ = stdout.Close()
		_ = stdin.Close()
	}()
	initCtx, initCancel := context.WithTimeout(ctx, s.StartupTimeout)
	defer initCancel()
	p.Init, err = acp.SendRequest[acp.InitializeResponse](p.rpc, initCtx, "initialize", acp.InitializeRequest{ProtocolVersion: 1, ClientInfo: &acp.Implementation{Name: "acpboss", Version: "dev"}})
	if err == nil && p.Init.ProtocolVersion != 1 {
		err = fmt.Errorf("unsupported ACP version")
	}
	if err != nil {
		closeCtx, cc := context.WithTimeout(context.Background(), s.ShutdownTimeout)
		defer cc()
		_ = p.Close(closeCtx)
		return nil, fmt.Errorf("provider %q: initialization failed: %w", c.ID, safeError(err))
	}
	return p, nil
}

// Call preserves raw response fields (including legacy metadata) using SDK RPC.
func (p *Process) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !p.Alive() {
		return nil, fmt.Errorf("provider %q unavailable", p.Config.ID)
	}
	raw, err := acp.SendRequest[json.RawMessage](p.rpc, ctx, method, params)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("provider %q: %w", p.Config.ID, safeError(err))
	}
	return raw, nil
}

// Prompt signals only after the complete request has been written, so a
// cancellation notification can never overtake its request on stdin.
func (p *Process) Prompt(ctx context.Context, sid string, blocks []any, sent chan struct{}) (json.RawMessage, error) {
	p.mu.Lock()
	if p.sent[sid] != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("provider session already active")
	}
	p.sent[sid] = sent
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.sent, sid); p.mu.Unlock() }()
	return p.Call(ctx, "session/prompt", map[string]any{"sessionId": sid, "prompt": blocks})
}

func (p *Process) observe(raw []byte) {
	var v struct {
		Method string
		Params struct {
			SessionID string `json:"sessionId"`
		}
	}
	if json.Unmarshal(raw, &v) != nil || v.Method != "session/prompt" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch := p.sent[v.Params.SessionID]; ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

type observedWriter struct {
	file    *os.File
	observe func([]byte)
}

func (w *observedWriter) Write(b []byte) (int, error) {
	n, err := w.file.Write(b)
	if err == nil && n == len(b) {
		w.observe(b)
	}
	return n, err
}
func (w *observedWriter) SetWriteDeadline(t time.Time) error { return w.file.SetWriteDeadline(t) }

// Notify sends a protocol notification.
func (p *Process) Notify(ctx context.Context, method string, params any) error {
	return p.rpc.SendNotification(ctx, method, params)
}

// Alive reports whether the owned process and connection are still usable.
func (p *Process) Alive() bool {
	select {
	case <-p.done:
		return false
	case <-p.rpc.Done():
		return false
	default:
		return true
	}
}

// Close is idempotent and bounded; one reaper owns Wait, including after timeout.
func (p *Process) Close(ctx context.Context) error {
	p.once.Do(func() {
		go func() {
			defer close(p.closed)
			_ = p.stdin.Close()
			timer := time.NewTimer(200 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-p.done:
			case <-timer.C:
			}
			killProcess(p.cmd)
			p.cancel()
			_ = p.stdout.Close()
			select {
			case <-p.done:
			case <-ctx.Done():
			}
		}()
	})
	select {
	case <-p.closed:
		return nil
	case <-ctx.Done():
		killProcess(p.cmd)
		p.cancel()
		return ctx.Err()
	}
}

func safeError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var re *acp.RequestError
	if errors.As(err, &re) {
		message := map[int]string{-32000: "authentication required", -32601: "method unsupported", -32602: "invalid upstream parameters"}[re.Code]
		if message == "" {
			message = "upstream RPC failed"
		}
		return &acp.RequestError{Code: re.Code, Message: message}
	}
	return errors.New("upstream RPC failed")
}
