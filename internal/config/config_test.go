package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func configFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadPrecedenceAndEnvironmentCase(t *testing.T) {
	p := configFile(t, `runtime:
  providers:
    custom:
      cmd: >-
        agent "{{.Model}}" "two words" 'single quoted' escaped\ space '$(literal)'
      model: org/model
      extra_args: [--stdio]
      inherit_env: false
      env: {API_KEY: secret, lowercase: value}
acpboss: {request_timeout: 11s}
profiles:
  fast:
    acpboss: {request_timeout: 12s}
`)
	t.Setenv("ACPBOSS_REQUEST_TIMEOUT", "13s")
	c, err := Load(Options{Path: p, Profile: "fast", Overrides: map[string]any{"request_timeout": "14s"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Settings.RequestTimeout != 14*time.Second {
		t.Fatal(c.Settings.RequestTimeout)
	}
	r, err := c.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r[0].Environment, []string{"API_KEY=secret", "lowercase=value"}) {
		t.Fatal("environment key case or policy lost")
	}
	if !reflect.DeepEqual(r[0].Command, []string{"agent", "org/model", "two words", "single quoted", "escaped space", "$(literal)", "--stdio"}) {
		t.Fatal(r[0].Command)
	}
	c, err = Load(Options{Path: p, Profile: "fast"})
	if err != nil || c.Settings.RequestTimeout != 13*time.Second {
		t.Fatal(c.Settings, err)
	}
	t.Setenv("ACPBOSS_REQUEST_TIMEOUT", "")
	c, err = Load(Options{Path: p, Profile: "fast"})
	if err != nil || c.Settings.RequestTimeout != 12*time.Second {
		t.Fatal(c.Settings, err)
	}
	c, err = Load(Options{Path: p})
	if err != nil || c.Settings.RequestTimeout != 11*time.Second {
		t.Fatal(c.Settings, err)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	base := "runtime:\n  providers:\n    custom:\n      cmd: agent\n"
	cases := map[string]string{
		"unknown field":       strings.Replace(base, "cmd: agent", "command: agent", 1),
		"invalid id":          strings.Replace(base, "custom:", "Custom:", 1),
		"duplicate":           base + "    custom: {cmd: other}\n",
		"invalid budget":      base + "acpboss: {startup_timeout: 0s}\n",
		"unknown settings":    base + "acpboss: {invented: true}\n",
		"bad env":             strings.Replace(base, "cmd: agent", "cmd: agent\n      env: {\"A=B\": secret}", 1),
		"missing command":     strings.Replace(base, "cmd: agent", "env: {}", 1),
		"empty command":       strings.Replace(base, "cmd: agent", "cmd: \"\"", 1),
		"empty argument":      strings.Replace(base, "cmd: agent", "cmd: 'agent \"\"'", 1),
		"unclosed quote":      strings.Replace(base, "cmd: agent", "cmd: 'agent \"unfinished'", 1),
		"dangling escape":     strings.Replace(base, "cmd: agent", "cmd: 'agent \\'", 1),
		"provider MCP config": base + "      mcp_servers: [unused]\n",
		"runtime MCP config":  base + "  mcp_servers: {}\n",
		"extra document":      base + "---\n{}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(Options{Path: configFile(t, body)}); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
