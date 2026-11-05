package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/acpboss/internal/testprovider"
)

func testRouter(t *testing.T) *Router {
	t.Helper()
	c := testprovider.Config(t, map[string]string{"alpha": "", "beta": "legacy"})
	r := New(c)
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := r.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestProviderSwitchRetainsSeparateHistory(t *testing.T) {
	r := testRouter(t)
	id, err := r.Create(t.Context(), r.Config.Settings.WorkingDir, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ model, want string }{{"alpha/org/model", "alpha:org/model:0"}, {"beta/org/model", "beta:org/model:0"}, {"alpha/other", "alpha:other:1"}} {
		if err := r.Select(t.Context(), id, tc.model); err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		_, err := r.Prompt(t.Context(), id, TextPrompt("history"), func(note map[string]any) error {
			if note["sessionId"] != id {
				t.Error("backend ID leaked")
			}
			text.WriteString(Text(note))
			return nil
		})
		if err != nil || text.String() != tc.want {
			t.Fatal(text.String(), err)
		}
	}
}

func TestStatelessHTTPAndPermissions(t *testing.T) {
	r := testRouter(t)
	first, err := r.PrepareHTTP(t.Context(), "alpha/org/model")
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.PrepareHTTP(t.Context(), "alpha/org/model")
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	a := r.sessions[first].bindings["alpha"].id
	b := r.sessions[second].bindings["alpha"].id
	r.mu.Unlock()
	if a == b {
		t.Fatal("HTTP backend session reused")
	}
	_, err = r.Prompt(t.Context(), first, TextPrompt("permission"), nil)
	if !errors.Is(err, ErrInteraction) {
		t.Fatal(err)
	}
	if err := r.Release(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Snapshot(first); err == nil {
		t.Fatal("response routes retained")
	}
	if _, err := r.PrepareHTTP(t.Context(), "bare-model"); !errors.Is(err, ErrUnknownModel) {
		t.Fatal(err)
	}
}

func TestPermissionOwnershipAndConcurrentIsolation(t *testing.T) {
	r := testRouter(t)
	var mu sync.Mutex
	calls := map[string]int{}
	permission := func(_ context.Context, p map[string]any) (any, error) {
		mu.Lock()
		calls[p["sessionId"].(string)]++
		mu.Unlock()
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "yes"}}, nil
	}
	ids := []string{}
	for range 2 {
		id, err := r.Create(t.Context(), r.Config.Settings.WorkingDir, nil, permission, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := r.Select(t.Context(), ids[1], "beta/org/model"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			_, err := r.Prompt(t.Context(), id, TextPrompt("permission"), func(note map[string]any) error {
				if note["sessionId"] != id {
					t.Error("cross-session update")
				}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, id := range ids {
		if calls[id] != 1 {
			t.Fatal(calls)
		}
	}
}

func TestCancellationBusyAndDeathIsolation(t *testing.T) {
	r := testRouter(t)
	id, err := r.PrepareHTTP(t.Context(), "alpha/org/model")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := r.Prompt(t.Context(), id, TextPrompt("hang"), nil); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		r.mu.Lock()
		active := r.sessions[id].active != nil
		r.mu.Unlock()
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prompt did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := r.Select(t.Context(), id, "alpha/other"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	r.Cancel(id)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel hung")
	}
	other, err := r.PrepareHTTP(t.Context(), "beta/org/model")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Prompt(t.Context(), other, TextPrompt("hello"), nil)
	if err != nil {
		t.Fatal("unrelated provider affected", err)
	}
	// Cancellation returns promptly; the backend remains busy until it replies.
	deadline = time.Now().Add(time.Second)
	for {
		r.mu.Lock()
		busy := r.sessions[id].busy
		r.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provider did not acknowledge cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := r.Prompt(t.Context(), id, TextPrompt("crash"), nil); err == nil || errors.Is(err, ErrBusy) {
		t.Fatal("crash was not submitted to the idle provider", err)
	}
	for _, m := range r.Catalog() {
		if m.ProviderID == "alpha" {
			t.Fatal("dead provider catalog retained")
		}
	}
}

func TestSlowConsumerIsBounded(t *testing.T) {
	r := testRouter(t)
	id, err := r.PrepareHTTP(t.Context(), "alpha/org/model")
	if err != nil {
		t.Fatal(err)
	}
	first := true
	_, err = r.Prompt(t.Context(), id, TextPrompt("burst"), func(map[string]any) error {
		if first {
			first = false
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	})
	if !errors.Is(err, ErrBackpressure) {
		t.Fatalf("unbounded or silently dropped updates: %v", err)
	}
	other, err := r.PrepareHTTP(t.Context(), "beta/org/model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Prompt(t.Context(), other, TextPrompt("ok"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestUnacknowledgedCancellationRetainsCapacity(t *testing.T) {
	c := testprovider.Config(t, map[string]string{"alpha": "unresponsive", "beta": ""})
	c.Settings.RequestTimeout = 100 * time.Millisecond
	c.Settings.ShutdownTimeout = 100 * time.Millisecond
	c.Settings.MaxInFlight = 1
	r := New(c)
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := r.PrepareHTTP(t.Context(), "alpha/org/model")
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.PrepareHTTP(t.Context(), "beta/org/model")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := r.Prompt(t.Context(), id, TextPrompt("hang"), func(map[string]any) error { started <- struct{}{}; return nil })
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}
	r.Cancel(id)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := r.Prompt(t.Context(), other, TextPrompt("ok"), nil); !errors.Is(err, ErrCapacity) {
		t.Fatal("unacknowledged prompt released capacity", err)
	}
	deadline := time.Now().Add(time.Second)
	for len(r.semaphore) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("RPC deadline did not release capacity")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := r.Prompt(t.Context(), id, TextPrompt("ok"), nil); !errors.Is(err, ErrUnknownModel) {
		t.Fatal("unacknowledged backend reused", err)
	}
	if _, err := r.Prompt(t.Context(), other, TextPrompt("ok"), nil); err != nil {
		t.Fatal("other provider unusable", err)
	}
}

func TestInactiveProviderCallbacksCannotEnterActiveTurn(t *testing.T) {
	r := testRouter(t)
	id, err := r.Create(t.Context(), r.Config.Settings.WorkingDir, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Select(t.Context(), id, "beta/org/model"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := r.Prompt(t.Context(), id, TextPrompt("hang"), func(note map[string]any) error {
			if Text(note) != "waiting" {
				t.Error("inactive provider text leaked", Text(note))
			}
			started <- struct{}{}
			return nil
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}
	r.mu.Lock()
	sid := r.sessions[id].bindings["alpha"].id
	r.mu.Unlock()
	update, _ := json.Marshal(map[string]any{"sessionId": sid, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "late alpha"}}})
	if _, err := r.handle(t.Context(), "alpha", "session/update", update); err != nil {
		t.Fatal(err)
	}
	permission, _ := json.Marshal(map[string]any{"sessionId": sid})
	if _, err := r.handle(t.Context(), "alpha", "session/request_permission", permission); err != nil {
		t.Fatal(err)
	}
	r.Cancel(id)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("inactive permission cancelled selected provider", err)
	}
}
