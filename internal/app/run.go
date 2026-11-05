// Package app connects CLI modes to their serving lifecycles.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/baldaworks/acpboss/internal/acp"
	"github.com/baldaworks/acpboss/internal/config"
	"github.com/baldaworks/acpboss/internal/openai"
	"github.com/baldaworks/acpboss/internal/proxy"
)

// Run starts one CLI serving mode.
func Run(ctx context.Context, mode string, c config.Config, in io.Reader, out, diagnostics io.Writer) error {
	r := proxy.New(c)
	if mode == "stdio" {
		return acp.Serve(ctx, r, in, out)
	}
	if mode != "api" {
		return fmt.Errorf("unknown serving mode")
	}
	defer func() { _ = r.Close() }()
	if err := r.Start(ctx); err != nil {
		return err
	}
	setup, cancel := context.WithTimeout(ctx, c.Settings.StartupTimeout)
	err := r.Discover(setup)
	cancel()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", c.Settings.HTTP.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: openai.New(r), ReadHeaderTimeout: min(10*time.Second, c.Settings.RequestTimeout), ReadTimeout: c.Settings.RequestTimeout, WriteTimeout: c.Settings.RequestTimeout, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer func() { _ = server.Close() }()
	_, _ = fmt.Fprintf(diagnostics, "acpboss listening on %s\n", listener.Addr())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), c.Settings.ShutdownTimeout)
	defer stop()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}
