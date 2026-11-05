// Package cli provides the acpboss command tree.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/baldaworks/acpboss/internal/config"
	"github.com/spf13/cobra"
)

// Run starts a serving mode with validated configuration and injected streams.
type Run func(context.Context, string, config.Config, io.Reader, io.Writer, io.Writer) error

// New constructs an independent CLI instance.
func New(in io.Reader, out, diagnostics io.Writer, run Run) *cobra.Command {
	root := &cobra.Command{Use: "acpboss", Short: "Proxy managed ACP providers through ACP or OpenAI Responses", Version: "dev", SilenceUsage: true, SilenceErrors: true}
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(diagnostics)
	var path, profile string
	root.PersistentFlags().StringVar(&path, "config", "", "Configuration file (default .config/acpboss/config.yaml)")
	root.PersistentFlags().StringVar(&profile, "profile", "", "Configuration profile")
	keys := map[string]string{"working-dir": "working_dir", "listen": "http.listen", "startup-timeout": "startup_timeout", "request-timeout": "request_timeout", "shutdown-timeout": "shutdown_timeout"}
	for _, key := range []string{"working-dir", "listen", "startup-timeout", "request-timeout", "shutdown-timeout"} {
		root.PersistentFlags().String(key, "", "Override "+keys[key])
	}
	for _, mode := range []string{"stdio", "api"} {
		cmd := &cobra.Command{Use: mode, Short: map[string]string{"stdio": "Serve ACP v1 over stdio", "api": "Serve OpenAI Responses over HTTP"}[mode], Args: cobra.NoArgs}
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			overrides := map[string]any{}
			for flag, key := range keys {
				if cmd.Flags().Changed(flag) {
					value, _ := cmd.Flags().GetString(flag)
					overrides[key] = value
				}
			}
			c, err := config.Load(config.Options{Path: path, Profile: profile, Overrides: overrides})
			if err != nil {
				return err
			}
			if run == nil {
				return fmt.Errorf("serving mode is not wired")
			}
			return run(cmd.Context(), cmd.Name(), c, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		}
		root.AddCommand(cmd)
	}
	return root
}
