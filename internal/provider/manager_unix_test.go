//go:build unix

package provider

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/baldaworks/acpboss/internal/config"
)

func TestStartupRollbackReapsFirstProvider(t *testing.T) {
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
	// Only the first process records its PID.
	z := c.Runtime.Providers["zulu"]
	delete(z.Env, "FIXTURE_PIDFILE")
	if m, err := StartAll(t.Context(), c, nil); err == nil {
		_ = m.Close()
		t.Fatal("failure did not roll startup back")
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("provider process still present: %d %v", pid, err)
	}
}
