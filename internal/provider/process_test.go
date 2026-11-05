package provider

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/baldaworks/acpboss/internal/config"
	"github.com/baldaworks/acpboss/internal/testprovider"
)

func TestACPFixtureProcess(t *testing.T) { testprovider.MaybeRun() }
func fixture(t *testing.T, id, mode string) config.Resolved {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return config.Resolved{ID: id, Command: []string{exe, "-test.run=^TestACPFixtureProcess$"}, Environment: append(os.Environ(), "ACPBOSS_FIXTURE=1", "FIXTURE_NAME="+id, "FIXTURE_MODE="+mode)}
}
func settings(t *testing.T) config.Settings {
	t.Helper()
	return config.Settings{WorkingDir: t.TempDir(), StartupTimeout: time.Second, RequestTimeout: time.Second, ShutdownTimeout: time.Second, MaxMessageBytes: 8 << 20}
}

func TestProcessLifetimeAndNotificationBarrier(t *testing.T) {
	s := settings(t)
	s.StartupTimeout = 500 * time.Millisecond
	var updates []string
	p, err := Start(t.Context(), fixture(t, "alpha", ""), s, func(_ context.Context, _ *Process, method string, raw json.RawMessage) (any, error) {
		if method == "session/update" {
			var n struct {
				Update struct{ Content struct{ Text string } }
			}
			if err := json.Unmarshal(raw, &n); err != nil {
				return nil, err
			}
			updates = append(updates, n.Update.Content.Text)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		if err := p.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	time.Sleep(100 * time.Millisecond)
	raw, err := p.Call(t.Context(), "session/new", map[string]any{"cwd": s.WorkingDir, "mcpServers": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(raw, &session)
	_, err = p.Call(t.Context(), "session/prompt", map[string]any{"sessionId": session.SessionID, "prompt": []any{map[string]any{"type": "text", "text": "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 || updates[1] != "hello" {
		t.Fatalf("response overtook notifications: %v", updates)
	}
}

func TestInitializeFailureAndClose(t *testing.T) {
	for _, mode := range []string{"fail-init", "hang-init"} {
		t.Run(mode, func(t *testing.T) {
			s := settings(t)
			s.StartupTimeout = 50 * time.Millisecond
			if p, err := Start(t.Context(), fixture(t, "bad", mode), s, nil); err == nil {
				_ = p.Close(t.Context())
				t.Fatal("invalid provider initialized")
			}
		})
	}
}

func TestProviderExitIsObserved(t *testing.T) {
	p, err := Start(t.Context(), fixture(t, "alpha", ""), settings(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Call(t.Context(), "session/prompt", map[string]any{"sessionId": "s", "prompt": []any{map[string]any{"type": "text", "text": "crash"}}})
	if err == nil {
		t.Fatal("crash succeeded")
	}
	if p.Alive() {
		t.Fatal("dead provider alive")
	}
	ctx, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
