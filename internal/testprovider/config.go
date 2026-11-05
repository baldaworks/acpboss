package testprovider

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/baldaworks/acpboss/internal/config"
)

// Config creates a fixture config for a test binary with TestACPFixtureProcess.
func Config(t testing.TB, modes map[string]string) config.Config {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := config.Config{Runtime: config.Runtime{Providers: map[string]config.Provider{}}, Settings: config.Settings{WorkingDir: t.TempDir(), StartupTimeout: 2 * time.Second, RequestTimeout: 3 * time.Second, ShutdownTimeout: 2 * time.Second, MaxInFlight: 4, MaxSessions: 32, MaxMessageBytes: 8 << 20}}
	c.Settings.HTTP.Listen = "127.0.0.1:0"
	for name, mode := range modes {
		c.Runtime.Providers[name] = config.Provider{Cmd: strconv.Quote(exe) + " -test.run=^TestACPFixtureProcess$", Env: map[string]string{"ACPBOSS_FIXTURE": "1", "FIXTURE_NAME": name, "FIXTURE_MODE": mode}}
	}
	return c
}
