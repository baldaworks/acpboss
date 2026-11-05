// Package acp exposes the proxy as an ACP v1 agent over stdio.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/baldaworks/acpboss/internal/protocol"
	"github.com/baldaworks/acpboss/internal/proxy"
	sdk "github.com/coder/acp-go-sdk"
)

type server struct {
	router      *proxy.Router
	conn        *sdk.Connection
	app         context.Context
	mu          sync.Mutex
	initialized bool
}

// Serve runs one ACP connection and shuts down all owned providers on EOF or cancellation.
func Serve(ctx context.Context, r *proxy.Router, in io.Reader, out io.Writer) error {
	app, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{router: r, app: app}
	conn, release := protocol.Open(s.handle, out, in, r.Config.Settings.MaxMessageBytes, r.Config.Settings.RequestTimeout)
	s.conn = conn
	release()
	select {
	case <-ctx.Done():
	case <-conn.Done():
	}
	cancel()
	if c, ok := in.(io.Closer); ok {
		_ = c.Close()
	}
	if c, ok := out.(io.Closer); ok {
		_ = c.Close()
	}
	return r.Close()
}

func (s *server) handle(ctx context.Context, method string, raw json.RawMessage) (any, *sdk.RequestError) {
	var p map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		return nil, sdk.NewInvalidParams(nil)
	}
	if method == "initialize" {
		v, ok := p["protocolVersion"].(float64)
		if !ok || v < 1 {
			return nil, sdk.NewInvalidParams(nil)
		}
		if err := s.router.Start(s.app); err != nil {
			return nil, protocol.RequestError(err)
		}
		s.mu.Lock()
		s.initialized = true
		s.mu.Unlock()
		return map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "acpboss", "version": "dev"}, "agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"close": map[string]any{}}, "promptCapabilities": s.router.PromptCapabilities()}, "authMethods": s.router.AuthenticationMethods()}, nil
	}
	s.mu.Lock()
	initialized := s.initialized
	s.mu.Unlock()
	if !initialized {
		return nil, sdk.NewInvalidRequest(nil)
	}
	ctx, cancel := context.WithTimeout(ctx, s.router.Config.Settings.RequestTimeout)
	defer cancel()
	sid, _ := p["sessionId"].(string)
	var value any
	var err error
	switch method {
	case "authenticate":
		id, ok := p["methodId"].(string)
		if !ok {
			return nil, sdk.NewInvalidParams(nil)
		}
		err = s.router.Authenticate(ctx, id)
		value = map[string]any{}
	case "session/new":
		cwd, ok := p["cwd"].(string)
		if !ok {
			return nil, sdk.NewInvalidParams(nil)
		}
		mcp, ok := p["mcpServers"].([]any)
		if p["mcpServers"] != nil && !ok {
			return nil, sdk.NewInvalidParams(nil)
		}
		var id string
		id, err = s.router.Create(ctx, cwd, mcp, func(ctx context.Context, params map[string]any) (any, error) {
			return sdk.SendRequest[json.RawMessage](s.conn, ctx, "session/request_permission", params)
		}, "")
		if err == nil {
			value, err = s.router.Snapshot(id)
		}
	case "session/set_config_option":
		if sid == "" {
			return nil, sdk.NewInvalidParams(nil)
		}
		if p["configId"] == "model" {
			model, ok := p["value"].(string)
			if !ok {
				return nil, sdk.NewInvalidParams(nil)
			}
			err = s.router.Select(ctx, sid, model)
			if err == nil {
				value, err = s.router.Snapshot(sid)
			}
		} else {
			value, err = s.router.SetControl(ctx, sid, method, p)
		}
	case "session/set_mode":
		value, err = s.router.SetControl(ctx, sid, method, p)
	case "session/prompt":
		blocks, ok := p["prompt"].([]any)
		if sid == "" || !ok || len(blocks) == 0 {
			return nil, sdk.NewInvalidParams(nil)
		}
		value, err = s.router.Prompt(ctx, sid, blocks, func(note map[string]any) error { return s.conn.SendNotification(ctx, "session/update", note) })
	case "session/cancel":
		s.router.Cancel(sid)
		return nil, nil
	case "session/close":
		err = s.router.Release(ctx, sid)
		value = map[string]any{}
	default:
		return nil, sdk.NewMethodNotFound(method)
	}
	if err != nil {
		var content *proxy.ContentError
		if errors.As(err, &content) {
			return nil, &sdk.RequestError{Code: -32602, Message: content.Error()}
		}
		return nil, protocol.RequestError(err)
	}
	return value, nil
}
