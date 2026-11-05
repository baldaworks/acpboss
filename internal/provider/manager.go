package provider

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"sync"
	"time"

	"github.com/baldaworks/acpboss/internal/config"
)

const (
	minRetryDelay = time.Second
	maxRetryDelay = 30 * time.Second
)

// LifecycleHooks connect provider lifecycle transitions to higher-level routing.
// Hooks must return promptly; only Manager owns closing a Process.
type LifecycleHooks struct {
	Probe         func(context.Context, *Process, uint64) error
	Ready         func(*Process, uint64)
	Unavailable   func(string, uint64)
	Transition    func(LifecycleEvent)
	retryDelayFor func(int) time.Duration
}

// LifecycleEvent is a sanitized provider state transition. It deliberately
// carries no command, environment, stderr, or upstream diagnostic payload.
type LifecycleEvent struct {
	ProviderID string
	Phase      string
	State      string
	Generation uint64
	Attempt    int
}

type lifecycleEntry struct {
	config      config.Resolved
	generation  uint64
	process     *Process
	ready       bool
	initial     chan struct{}
	initialOnce sync.Once
}

// Manager independently supervises every configured provider process.
type Manager struct {
	Settings config.Settings

	ctx         context.Context
	cancel      context.CancelFunc
	handler     Handler
	hooks       LifecycleHooks
	entries     map[string]*lifecycleEntry
	order       []string
	mu          sync.RWMutex
	wg          sync.WaitGroup
	closeOnce   sync.Once
	initialDone chan struct{}
}

// StartBackground starts provider supervisors without waiting for provider I/O.
// Static configuration errors are still returned synchronously.
func StartBackground(ctx context.Context, c config.Config, handler Handler, hooks LifecycleHooks) (*Manager, error) {
	return startAll(ctx, c, handler, hooks, false)
}

func startAll(ctx context.Context, c config.Config, handler Handler, hooks LifecycleHooks, waitInitial bool) (*Manager, error) {
	resolved, err := c.Resolve()
	if err != nil {
		return nil, err
	}
	managerCtx, cancel := context.WithCancel(ctx)
	m := &Manager{
		Settings:    c.Settings,
		ctx:         managerCtx,
		cancel:      cancel,
		handler:     handler,
		hooks:       hooks,
		entries:     make(map[string]*lifecycleEntry, len(resolved)),
		order:       make([]string, 0, len(resolved)),
		initialDone: make(chan struct{}),
	}
	for _, providerConfig := range resolved {
		entry := &lifecycleEntry{config: providerConfig, initial: make(chan struct{})}
		m.entries[providerConfig.ID] = entry
		m.order = append(m.order, providerConfig.ID)
		m.wg.Add(1)
		go m.supervise(entry)
	}
	m.wg.Add(1)
	go m.trackInitial()
	if waitInitial {
		if err := m.WaitInitial(ctx); err != nil {
			_ = m.Close()
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) trackInitial() {
	defer m.wg.Done()
	defer close(m.initialDone)
	for _, id := range m.order {
		select {
		case <-m.entries[id].initial:
		case <-m.ctx.Done():
			return
		}
	}
}

func (m *Manager) supervise(entry *lifecycleEntry) {
	defer m.wg.Done()
	attempt := 0
	for {
		if m.ctx.Err() != nil {
			entry.initialOnce.Do(func() { close(entry.initial) })
			return
		}
		attempt++
		generation := m.nextGeneration(entry)
		m.transition(entry.config.ID, generation, attempt, "start", "starting")
		process, err := m.start(entry.config)
		if err == nil && m.hooks.Probe != nil {
			probeCtx, cancel := context.WithTimeout(m.ctx, m.Settings.StartupTimeout)
			err = m.hooks.Probe(probeCtx, process, generation)
			cancel()
			if err != nil {
				m.closeProcess(process)
			}
		}
		if err != nil {
			m.transition(entry.config.ID, generation, attempt, "start", "retrying")
			entry.initialOnce.Do(func() { close(entry.initial) })
			if !m.retry(attempt) {
				return
			}
			continue
		}

		m.publish(entry, process, generation)
		m.transition(entry.config.ID, generation, attempt, "run", "ready")
		entry.initialOnce.Do(func() { close(entry.initial) })
		if m.hooks.Ready != nil {
			m.hooks.Ready(process, generation)
		}
		readyAttempt := attempt
		attempt = 0
		select {
		case <-m.ctx.Done():
		case <-process.done:
		case <-process.rpc.Done():
		}
		m.unpublish(entry, process, generation)
		state := "unavailable"
		if m.ctx.Err() != nil {
			state = "stopped"
		}
		m.transition(entry.config.ID, generation, readyAttempt, "run", state)
		m.closeProcess(process)
		if m.ctx.Err() != nil {
			return
		}
		if !m.retry(1) {
			return
		}
	}
}

func (m *Manager) transition(id string, generation uint64, attempt int, phase, state string) {
	if m.hooks.Transition != nil {
		m.hooks.Transition(LifecycleEvent{ProviderID: id, Phase: phase, State: state, Generation: generation, Attempt: attempt})
	}
}

func (m *Manager) start(providerConfig config.Resolved) (*Process, error) {
	if _, err := exec.LookPath(providerConfig.Command[0]); err != nil {
		return nil, fmt.Errorf("provider %q: executable not found", providerConfig.ID)
	}
	return Start(m.ctx, providerConfig, m.Settings, m.handler)
}

func (m *Manager) nextGeneration(entry *lifecycleEntry) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry.generation++
	return entry.generation
}

func (m *Manager) publish(entry *lifecycleEntry, process *Process, generation uint64) {
	m.mu.Lock()
	entry.process = process
	entry.generation = generation
	entry.ready = true
	m.mu.Unlock()
}

func (m *Manager) unpublish(entry *lifecycleEntry, process *Process, generation uint64) {
	m.mu.Lock()
	if entry.process == process && entry.generation == generation {
		entry.process = nil
		entry.ready = false
	}
	m.mu.Unlock()
	if m.hooks.Unavailable != nil {
		m.hooks.Unavailable(entry.config.ID, generation)
	}
}

func (m *Manager) retry(attempt int) bool {
	delay := retryDelay(attempt)
	if m.hooks.retryDelayFor != nil {
		delay = m.hooks.retryDelayFor(attempt)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-m.ctx.Done():
		return false
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := minRetryDelay
	for range attempt - 1 {
		if delay >= maxRetryDelay/2 {
			delay = maxRetryDelay
			break
		}
		delay *= 2
	}
	// Equal jitter prevents both busy loops and synchronized retry storms.
	half := delay / 2
	return half + time.Duration(rand.Int64N(int64(delay-half)+1))
}

// Snapshot returns currently ready processes in deterministic provider-ID order.
func (m *Manager) Snapshot() []*Process {
	m.mu.RLock()
	defer m.mu.RUnlock()
	processes := make([]*Process, 0, len(m.order))
	for _, id := range m.order {
		entry := m.entries[id]
		if entry.ready && entry.process != nil && entry.process.Alive() {
			processes = append(processes, entry.process)
		}
	}
	return processes
}

// Generation returns the currently published generation for process.
func (m *Manager) Generation(process *Process) (uint64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry := m.entries[process.Config.ID]
	if entry == nil || !entry.ready || entry.process != process {
		return 0, false
	}
	return entry.generation, true
}

// WaitInitial waits until every provider completed its first bounded attempt.
func (m *Manager) WaitInitial(ctx context.Context) error {
	select {
	case <-m.initialDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) closeProcess(process *Process) {
	ctx, cancel := context.WithTimeout(context.Background(), m.Settings.ShutdownTimeout)
	defer cancel()
	_ = process.Close(ctx)
}

// Close cancels retries and stops all owned children under one global budget.
func (m *Manager) Close() error {
	m.closeOnce.Do(m.cancel)
	ctx, cancel := context.WithTimeout(context.Background(), m.Settings.ShutdownTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
