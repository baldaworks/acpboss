package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/acpboss/internal/config"
)

func TestHelp(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"root", []string{"--help"}, []string{"stdio", "api", "--config"}},
		{"stdio", []string{"stdio", "--help"}, []string{"ACP", "stdio", "--config"}},
		{"api", []string{"api", "--help"}, []string{"Responses", "HTTP", "--config"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			c := New(nil, &out, &out, nil)
			c.SetArgs(tc.args)
			if err := c.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Fatal(out.String())
				}
			}
		})
	}
}

func TestFlagsAndIndependentInstances(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("runtime:\n  providers:\n    a:\n      cmd: agent\nprofiles:\n  fast:\n    acpboss: {request_timeout: 9s}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"stdio", "api"} {
		t.Run(mode, func(t *testing.T) {
			for i := range 3 {
				var out bytes.Buffer
				var got time.Duration
				c := New(nil, &out, &out, func(_ context.Context, selected string, c config.Config, _ io.Reader, _ io.Writer, _ io.Writer) error {
					if selected != mode {
						t.Fatal(selected)
					}
					got = c.Settings.RequestTimeout
					return nil
				})
				args := []string{mode, "--config", p}
				want := 5 * time.Minute
				if i < 2 {
					args = append(args, "--profile", "fast")
					want = 9 * time.Second
				}
				if i == 0 {
					args = append(args, "--request-timeout", "7s")
					want = 7 * time.Second
				}
				c.SetArgs(args)
				if err := c.Execute(); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("%v != %v", got, want)
				}
			}
		})
	}
}

func TestExtraArgumentsRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("runtime:\n  providers:\n    a:\n      cmd: agent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"stdio", "api"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			called := false
			c := New(nil, &out, &out, func(context.Context, string, config.Config, io.Reader, io.Writer, io.Writer) error {
				called = true
				return nil
			})
			c.SetArgs([]string{mode, "--config", p, "unexpected"})
			if err := c.Execute(); err == nil || called {
				t.Fatal("extra argument allowed serving to start", err)
			}
		})
	}
}
