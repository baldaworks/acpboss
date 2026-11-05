package acp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/acpboss/internal/protocol"
	"github.com/baldaworks/acpboss/internal/proxy"
	"github.com/baldaworks/acpboss/internal/testprovider"
	sdk "github.com/coder/acp-go-sdk"
)

func TestACPFixtureProcess(t *testing.T) { testprovider.MaybeRun() }

func TestACPWireSessionAndPermission(t *testing.T) {
	c := testprovider.Config(t, map[string]string{"alpha": "", "beta": ""})
	sessionFiles := map[string]string{}
	for id, p := range c.Runtime.Providers {
		path := filepath.Join(t.TempDir(), id+".json")
		p.Env["FIXTURE_SESSIONFILE"] = path
		sessionFiles[id] = path
	}
	r := proxy.New(c)
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, r, b, b) }()
	var updates []map[string]any
	permissions := 0
	conn, release := protocol.Open(func(_ context.Context, method string, raw json.RawMessage) (any, *sdk.RequestError) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		switch method {
		case "session/update":
			updates = append(updates, p)
			return nil, nil
		case "session/request_permission":
			permissions++
			if !strings.HasPrefix(p["sessionId"].(string), "session-") {
				t.Error("backend session leaked")
			}
			return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "yes"}}, nil
		}
		return nil, sdk.NewMethodNotFound(method)
	}, a, a, 8<<20, time.Second)
	release()
	call := func(method string, p any) map[string]any {
		t.Helper()
		v, err := sdk.SendRequest[map[string]any](conn, t.Context(), method, p)
		if err != nil {
			t.Fatal(method, err)
		}
		return v
	}
	init := call("initialize", map[string]any{"protocolVersion": 1})
	if init["protocolVersion"] != float64(1) {
		t.Fatal(init)
	}
	call("authenticate", map[string]any{"methodId": "alpha/login"})
	mcp := []any{
		map[string]any{"name": "client-stdio", "command": "client-owned-mcp", "args": []any{"--stdio"}, "env": []any{map[string]any{"name": "CaseSensitive", "value": "fixture"}}},
		map[string]any{"name": "client-http", "type": "http", "url": "http://localhost:9999/mcp", "headers": []any{map[string]any{"name": "X-Client", "value": "fixture"}}, "_meta": map[string]any{"clientExtension": true}},
	}
	session := call("session/new", map[string]any{"cwd": c.Settings.WorkingDir, "mcpServers": mcp})
	for provider, path := range sessionFiles {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var received map[string]any
		if err := json.Unmarshal(raw, &received); err != nil {
			t.Fatal(err)
		}
		if received["cwd"] != c.Settings.WorkingDir || !reflect.DeepEqual(received["mcpServers"], mcp) {
			t.Fatal("client session parameters changed for", provider, received)
		}
	}
	id := session["sessionId"].(string)
	call("session/set_config_option", map[string]any{"sessionId": id, "configId": "model", "value": "beta/org/model"})
	result := call("session/prompt", map[string]any{"sessionId": id, "prompt": proxy.TextPrompt("permission")})
	if result["stopReason"] != "end_turn" || permissions != 1 || len(updates) != 2 {
		t.Fatal(result, permissions, updates)
	}
	for _, u := range updates {
		if u["sessionId"] != id {
			t.Fatal(u)
		}
	}
	if _, err := sdk.SendRequest[map[string]any](conn, t.Context(), "session/load", map[string]any{"sessionId": id}); err == nil {
		t.Fatal("unsupported load succeeded")
	}
	call("session/close", map[string]any{"sessionId": id})
	_ = a.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("EOF cleanup hung")
	}
}

func TestACPImageWire(t *testing.T) {
	c := testprovider.Config(t, map[string]string{"alpha": "media", "beta": ""})
	r := proxy.New(c)
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, r, b, b) }()
	defer func() {
		cancel()
		_ = a.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var content map[string]any
	conn, release := protocol.Open(func(_ context.Context, method string, raw json.RawMessage) (any, *sdk.RequestError) {
		if method == "session/update" {
			var note map[string]any
			_ = json.Unmarshal(raw, &note)
			content = note["update"].(map[string]any)["content"].(map[string]any)
		}
		return nil, nil
	}, a, a, 8<<20, time.Second)
	release()
	call := func(method string, params any) map[string]any {
		t.Helper()
		v, err := sdk.SendRequest[map[string]any](conn, t.Context(), method, params)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	init := call("initialize", map[string]any{"protocolVersion": 1})
	caps := init["agentCapabilities"].(map[string]any)["promptCapabilities"].(map[string]any)
	if caps["image"] != true || caps["audio"] != true || caps["embeddedContext"] != true {
		t.Fatal(caps)
	}
	session := call("session/new", map[string]any{"cwd": c.Settings.WorkingDir, "mcpServers": []any{}})
	id := session["sessionId"].(string)
	image := map[string]any{"type": "image", "mimeType": "image/png", "data": "YQ==", "_meta": map[string]any{"key": "preserve"}}
	call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{image}})
	if !reflect.DeepEqual(content, image) {
		t.Fatal("image update changed", content)
	}
	call("session/set_config_option", map[string]any{"sessionId": id, "configId": "model", "value": "beta/org/model"})
	_, err := sdk.SendRequest[map[string]any](conn, t.Context(), "session/prompt", map[string]any{"sessionId": id, "prompt": []any{image}})
	var rpc *sdk.RequestError
	if !errors.As(err, &rpc) || rpc.Code != -32602 || !strings.Contains(rpc.Message, "unsupported") {
		t.Fatal("missing useful capability error", err)
	}
}
