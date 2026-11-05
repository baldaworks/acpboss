// Package config loads provider commands and proxy settings.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/shlex"
)

// Config is the validated application configuration.
type Config struct {
	Runtime  Runtime
	Settings Settings
}

// Runtime groups the configured provider processes.
type Runtime struct {
	Providers map[string]Provider `yaml:"providers"`
}

// Provider describes one ACP subprocess and its session defaults.
type Provider struct {
	Cmd                     string            `yaml:"cmd"`
	ExtraArgs               []string          `yaml:"extra_args"`
	Env                     map[string]string `yaml:"env"`
	InheritEnv              *bool             `yaml:"inherit_env"`
	Model                   string            `yaml:"model"`
	ModelConfigID           string            `yaml:"model_config_id"`
	Mode                    string            `yaml:"mode"`
	ReasoningEffort         string            `yaml:"reasoning_effort"`
	ReasoningEffortConfigID string            `yaml:"reasoning_effort_config_id"`
	SystemInstructions      string            `yaml:"system_instructions"`
}

// Settings contains proxy-specific settings and finite budgets.
type Settings struct {
	WorkingDir string `mapstructure:"working_dir"`
	HTTP       struct {
		Listen string `mapstructure:"listen"`
	} `mapstructure:"http"`
	StartupTimeout  time.Duration `mapstructure:"startup_timeout"`
	RequestTimeout  time.Duration `mapstructure:"request_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	MaxInFlight     int           `mapstructure:"max_in_flight"`
	MaxSessions     int           `mapstructure:"max_sessions"`
	MaxMessageBytes int           `mapstructure:"max_message_bytes"`
}

// Resolved is an executable provider configuration. Environment values are never logged.
type Resolved struct {
	ID           string
	Command      []string
	Environment  []string
	Defaults     Provider
	Instructions string
}

var providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Resolve validates all providers before a process can be launched.
func (c Config) Resolve() ([]Resolved, error) {
	if len(c.Runtime.Providers) == 0 {
		return nil, fmt.Errorf("runtime.providers must not be empty")
	}
	if c.Settings.StartupTimeout <= 0 || c.Settings.RequestTimeout <= 0 || c.Settings.ShutdownTimeout <= 0 {
		return nil, fmt.Errorf("timeouts must be positive")
	}
	if c.Settings.MaxInFlight <= 0 || c.Settings.MaxSessions <= 0 || c.Settings.MaxMessageBytes < 1024 {
		return nil, fmt.Errorf("limits must be positive; max_message_bytes must be at least 1024")
	}
	if _, _, err := net.SplitHostPort(c.Settings.HTTP.Listen); err != nil {
		return nil, fmt.Errorf("acpboss.http.listen: %w", err)
	}
	info, err := os.Stat(c.Settings.WorkingDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("acpboss.working_dir must be an existing directory")
	}
	ids := make([]string, 0, len(c.Runtime.Providers))
	for id := range c.Runtime.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var result []Resolved
	for _, id := range ids {
		if !providerID.MatchString(id) {
			return nil, fmt.Errorf("invalid provider ID %q", id)
		}
		p := c.Runtime.Providers[id]
		cmd, err := p.command()
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", id, err)
		}
		env, err := p.environment(os.Environ())
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", id, err)
		}
		result = append(result, Resolved{ID: id, Command: cmd, Environment: env, Defaults: p, Instructions: p.SystemInstructions})
	}
	return result, nil
}

func (p Provider) command() ([]string, error) {
	if len(p.Cmd) == 0 {
		return nil, fmt.Errorf("cmd is required")
	}
	if p.ReasoningEffort != "" && !contains([]string{"minimal", "low", "medium", "high", "xhigh"}, p.ReasoningEffort) {
		return nil, fmt.Errorf("invalid reasoning_effort")
	}
	cmd, err := shlex.Split(p.Cmd)
	if err != nil {
		return nil, fmt.Errorf("cmd has invalid quoting or escaping")
	}
	if len(cmd) == 0 {
		return nil, fmt.Errorf("cmd is required")
	}
	cmd = append(cmd, p.ExtraArgs...)
	for i, arg := range cmd {
		if strings.TrimSpace(arg) == "" || strings.ContainsRune(arg, 0) {
			return nil, fmt.Errorf("invalid command argument")
		}
		cmd[i] = strings.ReplaceAll(arg, "{{.Model}}", p.Model)
	}
	return cmd, nil
}

func (a Provider) environment(base []string) ([]string, error) {
	values := map[string]string{}
	if a.InheritEnv == nil || *a.InheritEnv {
		for _, v := range base {
			k, v, ok := strings.Cut(v, "=")
			if ok {
				values[k] = v
			}
		}
	}
	for k, v := range a.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return nil, fmt.Errorf("invalid process environment")
		}
		values[k] = v
	}
	env := make([]string, 0, len(values))
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return env, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func absoluteDir(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	return filepath.Abs(dir)
}
