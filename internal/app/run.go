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
	"github.com/baldaworks/acpboss/internal/provider"
	"github.com/baldaworks/acpboss/internal/proxy"
)

// Run starts one CLI serving mode.
func Run(ctx context.Context, mode string, c config.Config, in io.Reader, out, diagnostics io.Writer) error {
	appCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := proxy.New(c)
	go writeLifecycleDiagnostics(appCtx, diagnostics, r.LifecycleEvents())
	if mode == "stdio" {
		return acp.Serve(appCtx, r, in, out)
	}
	if mode != "api" {
		return fmt.Errorf("unknown serving mode")
	}
	defer func() { _ = r.Close() }()
	r.EnableDiscovery()
	if err := r.Start(appCtx); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", c.Settings.HTTP.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: openai.New(r), ReadHeaderTimeout: min(10*time.Second, c.Settings.RequestTimeout), ReadTimeout: c.Settings.RequestTimeout, WriteTimeout: c.Settings.RequestTimeout, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return appCtx }}
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
	case <-appCtx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), c.Settings.ShutdownTimeout)
	defer stop()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}

func writeLifecycleDiagnostics(ctx context.Context, diagnostics io.Writer, events <-chan provider.LifecycleEvent) {
	for {
		select {
		case event := <-events:
			_, _ = fmt.Fprintf(diagnostics, "provider %q lifecycle state=%s phase=%s attempt=%d generation=%d\n", event.ProviderID, event.State, event.Phase, event.Attempt, event.Generation)
		case <-ctx.Done():
			return
		}
	}
}
