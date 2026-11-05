package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
)

// Options selects the config document, profile and explicit CLI overrides.
type Options struct {
	Path, Profile string
	Overrides     map[string]any
}

// Load applies flags > ACPBOSS_* environment > profile/file > defaults.
// Provider environment map keys bypass Viper so case is preserved exactly.
func Load(opts Options) (Config, error) {
	path := opts.Path
	if path == "" {
		path = ".config/acpboss/config.yaml"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	b, err = expand(b)
	if err != nil {
		return Config{}, err
	}
	var root map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&root); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Config{}, fmt.Errorf("config must contain exactly one YAML document")
	}
	for k := range root {
		if k != "runtime" && k != "acpboss" && k != "profiles" {
			return Config{}, fmt.Errorf("unknown root key %q", k)
		}
	}
	profile := opts.Profile
	if profile == "" {
		profile = "default"
	}
	if profiles, ok := root["profiles"].(map[string]any); ok {
		if p, ok := profiles[profile].(map[string]any); ok {
			for k := range p {
				if k != "runtime" && k != "acpboss" {
					return Config{}, fmt.Errorf("unknown profile key %q", k)
				}
			}
			merge(root, p)
		} else if opts.Profile != "" {
			return Config{}, fmt.Errorf("unknown profile %q", profile)
		}
	} else if opts.Profile != "" {
		return Config{}, fmt.Errorf("unknown profile %q", profile)
	}
	var c Config
	raw, err := yaml.Marshal(root["runtime"])
	if err != nil {
		return c, fmt.Errorf("encode runtime: %w", err)
	}
	d := yaml.NewDecoder(bytes.NewReader(raw))
	d.KnownFields(true)
	if err := d.Decode(&c.Runtime); err != nil {
		return c, fmt.Errorf("runtime config: %w", err)
	}
	v := viper.New()
	v.SetConfigType("yaml")
	defaults := map[string]any{"working_dir": ".", "http.listen": "127.0.0.1:8080", "startup_timeout": "60s", "request_timeout": "5m", "shutdown_timeout": "10s", "max_in_flight": 32, "max_sessions": 128, "max_message_bytes": 8388608}
	for k, value := range defaults {
		v.SetDefault(k, value)
		if err := v.BindEnv(k, "ACPBOSS_"+strings.ToUpper(strings.ReplaceAll(k, ".", "_"))); err != nil {
			return c, err
		}
	}
	settings, err := yaml.Marshal(root["acpboss"])
	if err != nil {
		return c, err
	}
	if err := v.ReadConfig(bytes.NewReader(settings)); err != nil {
		return c, fmt.Errorf("acpboss config: %w", err)
	}
	for k, value := range opts.Overrides {
		v.Set(k, value)
	}
	if err := v.UnmarshalExact(&c.Settings); err != nil {
		return c, fmt.Errorf("acpboss settings: %w", err)
	}
	c.Settings.WorkingDir, err = absoluteDir(c.Settings.WorkingDir)
	if err != nil {
		return c, err
	}
	if _, err := c.Resolve(); err != nil {
		return c, err
	}
	return c, nil
}

func merge(dst, src map[string]any) {
	for k, v := range src {
		a, ok := dst[k].(map[string]any)
		b, bok := v.(map[string]any)
		if ok && bok {
			merge(a, b)
		} else {
			dst[k] = v
		}
	}
}

var expansion = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

func expand(b []byte) ([]byte, error) {
	var missing string
	result := expansion.ReplaceAllStringFunc(string(b), func(s string) string {
		m := expansion.FindStringSubmatch(s)
		if v, ok := os.LookupEnv(m[1]); ok && v != "" {
			return v
		}
		if strings.Contains(s, ":-") {
			return m[2]
		}
		missing = m[1]
		return ""
	})
	if missing != "" {
		return nil, fmt.Errorf("config environment variable %s is unset or empty", missing)
	}
	return []byte(result), nil
}
