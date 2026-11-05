package provider

import (
	"context"
	"fmt"
	"os/exec"
	"sync"

	"github.com/baldaworks/acpboss/internal/config"
)

// Manager owns all configured provider processes. Its membership is immutable after Start.
type Manager struct {
	Processes []*Process
	Settings  config.Settings
}

// StartAll preflights every executable before launching any process, then initializes all.
func StartAll(ctx context.Context, c config.Config, handler Handler) (*Manager, error) {
	resolved, err := c.Resolve()
	if err != nil {
		return nil, err
	}
	for _, p := range resolved {
		if _, err := exec.LookPath(p.Command[0]); err != nil {
			return nil, fmt.Errorf("provider %q: executable not found", p.ID)
		}
	}
	m := &Manager{Settings: c.Settings}
	for _, cfg := range resolved {
		p, err := Start(ctx, cfg, c.Settings, handler)
		if err != nil {
			_ = m.Close()
			return nil, err
		}
		m.Processes = append(m.Processes, p)
	}
	return m, nil
}

// Close stops owned children in parallel under one global shutdown budget.
func (m *Manager) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), m.Settings.ShutdownTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, p := range m.Processes {
		wg.Go(func() { _ = p.Close(ctx) })
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
