//go:build unix

package provider

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/baldaworks/acpboss/internal/config"
)

func TestProviderFailureDoesNotRollbackHealthyProvider(t *testing.T) {
	s := settings(t)
	s.HTTP.Listen = "127.0.0.1:0"
	s.MaxInFlight = 2
	s.MaxSessions = 8
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidfile := filepath.Join(t.TempDir(), "pid")
	makeProvider := func(mode string) config.Provider {
		return config.Provider{Cmd: strconv.Quote(exe) + " -test.run=^TestACPFixtureProcess$", Env: map[string]string{"ACPBOSS_FIXTURE": "1", "FIXTURE_MODE": mode, "FIXTURE_PIDFILE": pidfile}}
	}
	c := config.Config{Settings: s, Runtime: config.Runtime{Providers: map[string]config.Provider{"alpha": makeProvider(""), "zulu": makeProvider("fail-init")}}}
	zulu := c.Runtime.Providers["zulu"]
	delete(zulu.Env, "FIXTURE_PIDFILE")
	c.Runtime.Providers["zulu"] = zulu

	m, err := startAll(t.Context(), c, nil, LifecycleHooks{retryDelayFor: func(int) time.Duration { return time.Hour }}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot(); len(got) != 1 || got[0].Config.ID != "alpha" {
		t.Fatalf("ready providers = %v", providerIDs(got))
	}
	pid := readPID(t, pidfile)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("healthy provider was rolled back: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH })
}

func TestProviderRetriesMissingExecutableAndRecovers(t *testing.T) {
	s := settings(t)
	s.HTTP.Listen = "127.0.0.1:0"
	s.MaxInFlight = 2
	s.MaxSessions = 8
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(t.TempDir(), "provider")
	events := make(chan LifecycleEvent, 16)
	c := config.Config{Settings: s, Runtime: config.Runtime{Providers: map[string]config.Provider{
		"alpha": {Cmd: strconv.Quote(command) + " -test.run=^TestACPFixtureProcess$", Env: map[string]string{"ACPBOSS_FIXTURE": "1", "FIXTURE_NAME": "alpha"}},
	}}}
	m, err := startAll(t.Context(), c, nil, LifecycleHooks{
		Transition:    func(event LifecycleEvent) { events <- event },
		retryDelayFor: func(int) time.Duration { return 5 * time.Millisecond },
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if got := m.Snapshot(); len(got) != 0 {
		t.Fatalf("missing executable published: %v", providerIDs(got))
	}
	waitState(t, events, "retrying")
	if err := os.Symlink(exe, command); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return len(m.Snapshot()) == 1 })
	waitState(t, events, "ready")
}

func TestProviderExitStartsNewGeneration(t *testing.T) {
	s := settings(t)
	s.HTTP.Listen = "127.0.0.1:0"
	s.MaxInFlight = 2
	s.MaxSessions = 8
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := config.Config{Settings: s, Runtime: config.Runtime{Providers: map[string]config.Provider{
		"alpha": {Cmd: strconv.Quote(exe) + " -test.run=^TestACPFixtureProcess$", Env: map[string]string{"ACPBOSS_FIXTURE": "1", "FIXTURE_NAME": "alpha"}},
	}}}
	m, err := startAll(t.Context(), c, nil, LifecycleHooks{retryDelayFor: func(int) time.Duration { return 5 * time.Millisecond }}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	first := m.Snapshot()[0]
	_, _ = first.Call(t.Context(), "session/prompt", map[string]any{"sessionId": "missing", "prompt": []any{map[string]any{"type": "text", "text": "crash"}}})
	eventually(t, func() bool {
		current := m.Snapshot()
		return len(current) == 1 && current[0] != first
	})
}

func TestManagerCloseInterruptsBackoff(t *testing.T) {
	s := settings(t)
	s.HTTP.Listen = "127.0.0.1:0"
	s.MaxInFlight = 2
	s.MaxSessions = 8
	c := config.Config{Settings: s, Runtime: config.Runtime{Providers: map[string]config.Provider{
		"alpha": {Cmd: filepath.Join(t.TempDir(), "missing")},
	}}}
	m, err := startAll(t.Context(), c, nil, LifecycleHooks{retryDelayFor: func(int) time.Duration { return time.Hour }}, true)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("close did not interrupt backoff: %v", elapsed)
	}
}

func providerIDs(processes []*Process) []string {
	ids := make([]string, 0, len(processes))
	for _, process := range processes {
		ids = append(ids, process.Config.ID)
	}
	return ids
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied")
}

func waitState(t *testing.T, events <-chan LifecycleEvent, state string) {
	t.Helper()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case event := <-events:
			if event.State == state {
				return
			}
		case <-timeout.C:
			t.Fatalf("lifecycle state %q was not observed", state)
		}
	}
}
