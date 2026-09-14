package authpool

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Refresher is the interface for token refresh implementations.
type Refresher interface {
	// Refresh refreshes the token for a profile.
	// Returns the new token expiry time on success.
	Refresh(ctx context.Context, provider, profile string) (time.Time, error)
}

// MonitorConfig configures the background monitor loop.
type MonitorConfig struct {
	// CheckInterval is how often to check profile states.
	// Default: 1 minute
	CheckInterval time.Duration

	// RefreshThreshold is how early to refresh before expiry.
	// Default: 5 minutes (from AuthPool)
	RefreshThreshold time.Duration

	// MaxConcurrent limits concurrent refresh operations.
	// Default: 3
	MaxConcurrent int

	// OnRefreshStart is called when a refresh starts.
	OnRefreshStart func(provider, profile string)

	// OnRefreshComplete is called when a refresh completes.
	OnRefreshComplete func(provider, profile string, newExpiry time.Time, err error)
}

// DefaultMonitorConfig returns the default monitor configuration.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{
		CheckInterval:    time.Minute,
		RefreshThreshold: 5 * time.Minute,
		MaxConcurrent:    3,
	}
}

// Monitor runs a background token monitoring loop.
type Monitor struct {
	pool      *AuthPool
	refresher Refresher
	config    MonitorConfig

	// State
	mu        sync.Mutex
	running   bool
	stopping  bool // Set when Stop() is called, prevents new refreshes
	stopCh    chan struct{}
	stopOnce  sync.Once // Ensures stopCh is only closed once
	refreshWg sync.WaitGroup
	semaphore chan struct{}
}

// NewMonitor creates a new token monitor.
// Panics if pool is nil.
func NewMonitor(pool *AuthPool, refresher Refresher, config MonitorConfig) *Monitor {
	if pool == nil {
		panic("authpool: NewMonitor called with nil pool")
	}

	if config.CheckInterval == 0 {
		config.CheckInterval = time.Minute
	}
	if config.RefreshThreshold == 0 {
		config.RefreshThreshold = pool.refreshThreshold
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = 3
	}

	return &Monitor{
		pool:      pool,
		refresher: refresher,
		config:    config,
		semaphore: make(chan struct{}, config.MaxConcurrent),
	}
}

// Start begins the background monitoring loop.
// Returns immediately. Use Stop() to stop the loop.
func (m *Monitor) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return fmt.Errorf("monitor already running")
	}

	m.running = true
	m.stopping = false // Reset stopping flag for new cycle
	m.stopCh = make(chan struct{})
	m.stopOnce = sync.Once{} // Reset for new Start cycle

	go m.runLoop(ctx)
	return nil
}

// Stop stops the background monitoring loop.
// Waits for any in-flight refresh operations to complete.
func (m *Monitor) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}

	// Set stopping flag BEFORE releasing lock to prevent new refreshes
	// from starting. This prevents race with refreshWg.Wait() below.
	m.stopping = true
	m.running = false

	// Use sync.Once to ensure stopCh is only closed once, preventing panic
	// if Stop() is called concurrently or multiple times.
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	m.mu.Unlock()

	// Wait for in-flight refreshes to complete
	m.refreshWg.Wait()
}

// IsRunning returns whether the monitor is running.
func (m *Monitor) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// runLoop is the main monitoring loop.
func (m *Monitor) runLoop(ctx context.Context) {
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	// The loop keeps the cooldown bookkeeping current. It never spends a
	// refresh token on a tick: a refresh consumes it, and the families
	// rotate. Refreshing is an explicit ask (RefreshAll), for what has
	// expired.
	m.pool.CheckAndUpdateCooldowns()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.pool.CheckAndUpdateCooldowns()
		}
	}
}

// refreshExpired refreshes the profiles whose token has expired, and
// nothing else: not one with time left (spending it early gains nothing),
// and not one whose last refresh was refused — re-presenting a refused
// token is what trips reuse detection, so a refusal is terminal until a
// person acts.
func (m *Monitor) refreshExpired(ctx context.Context) {
	m.pool.CheckAndUpdateCooldowns()
	for _, profile := range m.pool.GetProfilesNeedingRefresh("") {
		if profile.Status == PoolStatusRefreshing || profile.Status == PoolStatusError {
			continue
		}
		expired := profile.Status == PoolStatusExpired || profile.IsExpired()
		if expired {
			m.triggerRefresh(ctx, profile.Provider, profile.ProfileName, profile.Status)
		}
	}
}

// triggerRefresh starts a refresh operation for a profile.
func (m *Monitor) triggerRefresh(ctx context.Context, provider, profile string, prevStatus PoolStatus) {
	// Try to acquire semaphore (non-blocking)
	select {
	case m.semaphore <- struct{}{}:
		// Got slot, proceed
	default:
		// No slots available, skip this refresh cycle
		return
	}

	if !m.pool.TryMarkRefreshing(provider, profile) {
		<-m.semaphore
		return
	}

	// Check if we're stopping before adding to WaitGroup to prevent race
	// with Stop() calling refreshWg.Wait(). Must hold lock to safely check
	// stopping flag and add to WaitGroup atomically.
	// Note: we check stopping (not running) to allow RefreshAll to work
	// when the monitor hasn't been started.
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		// Revert the optimistic status change so the pool doesn't stay stuck.
		if prevStatus != PoolStatusRefreshing {
			_ = m.pool.SetStatus(provider, profile, prevStatus)
		}
		<-m.semaphore
		return
	}
	m.refreshWg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.refreshWg.Done()
		defer func() { <-m.semaphore }()

		m.doRefresh(ctx, provider, profile)
	}()
}

// doRefresh performs the actual refresh operation.
func (m *Monitor) doRefresh(ctx context.Context, provider, profile string) {
	// Mark as refreshing
	if err := m.pool.SetStatus(provider, profile, PoolStatusRefreshing); err != nil {
		return // Profile may have been removed
	}

	// Call start callback
	if m.config.OnRefreshStart != nil {
		m.config.OnRefreshStart(provider, profile)
	}

	// Check if refresher is available
	if m.refresher == nil {
		m.pool.SetError(provider, profile, fmt.Errorf("no refresher configured"))
		if m.config.OnRefreshComplete != nil {
			m.config.OnRefreshComplete(provider, profile, time.Time{}, fmt.Errorf("no refresher configured"))
		}
		return
	}

	// Perform refresh
	newExpiry, err := m.refresher.Refresh(ctx, provider, profile)

	if err != nil {
		m.pool.SetError(provider, profile, err)
		if m.config.OnRefreshComplete != nil {
			m.config.OnRefreshComplete(provider, profile, time.Time{}, err)
		}
		return
	}

	// Success - update pool state
	m.pool.MarkRefreshed(provider, profile, newExpiry)

	if m.config.OnRefreshComplete != nil {
		m.config.OnRefreshComplete(provider, profile, newExpiry, nil)
	}
}

// ForceRefresh triggers an immediate refresh for a specific profile.
// This respects the MaxConcurrent semaphore to prevent overwhelming the system.
func (m *Monitor) ForceRefresh(ctx context.Context, provider, profile string) error {
	// Acquire semaphore slot (blocking, with context cancellation)
	select {
	case m.semaphore <- struct{}{}:
		// Got slot, proceed
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.semaphore }()

	// Participate in WaitGroup for graceful shutdown
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return fmt.Errorf("monitor is stopping")
	}
	m.refreshWg.Add(1)
	m.mu.Unlock()
	defer m.refreshWg.Done()

	// Try to mark as refreshing. This checks existence AND current status atomically.
	// We do this AFTER acquiring the semaphore to ensure we don't hold the lock
	// while waiting for the semaphore, but also to prevent races where another
	// routine starts refreshing while we wait.
	if !m.pool.TryMarkRefreshing(provider, profile) {
		// Need to distinguish "not found" vs "already refreshing"
		p := m.pool.GetProfile(provider, profile)
		if p == nil {
			return fmt.Errorf("profile %s/%s not found", provider, profile)
		}
		return fmt.Errorf("profile %s/%s already refreshing", provider, profile)
	}

	// Perform refresh (synchronous)
	m.doRefresh(ctx, provider, profile)

	// Check result
	p := m.pool.GetProfile(provider, profile)
	if p != nil && p.Status == PoolStatusError {
		return fmt.Errorf("refresh failed: %s", p.ErrorMessage)
	}

	return nil
}

// RefreshAll refreshes every profile whose token has expired; see
// refreshExpired for what it leaves alone.
func (m *Monitor) RefreshAll(ctx context.Context) {
	m.refreshExpired(ctx)
}

// Stats returns current monitor statistics.
type MonitorStats struct {
	Running          bool          `json:"running"`
	CheckInterval    time.Duration `json:"check_interval"`
	RefreshThreshold time.Duration `json:"refresh_threshold"`
	MaxConcurrent    int           `json:"max_concurrent"`
	ActiveRefreshes  int           `json:"active_refreshes"`
}

// Stats returns current monitor statistics.
func (m *Monitor) Stats() MonitorStats {
	m.mu.Lock()
	running := m.running
	m.mu.Unlock()

	// Count active refreshes (approximate - semaphore capacity minus available)
	activeRefreshes := len(m.semaphore)

	return MonitorStats{
		Running:          running,
		CheckInterval:    m.config.CheckInterval,
		RefreshThreshold: m.config.RefreshThreshold,
		MaxConcurrent:    m.config.MaxConcurrent,
		ActiveRefreshes:  activeRefreshes,
	}
}

// WithCheckInterval sets the check interval option.
func WithCheckInterval(d time.Duration) func(*MonitorConfig) {
	return func(c *MonitorConfig) {
		c.CheckInterval = d
	}
}

// WithMonitorRefreshThreshold sets the refresh threshold option.
func WithMonitorRefreshThreshold(d time.Duration) func(*MonitorConfig) {
	return func(c *MonitorConfig) {
		c.RefreshThreshold = d
	}
}

// WithMaxConcurrent sets the max concurrent refreshes option.
func WithMaxConcurrent(n int) func(*MonitorConfig) {
	return func(c *MonitorConfig) {
		c.MaxConcurrent = n
	}
}

// WithOnRefreshStart sets the refresh start callback.
func WithOnRefreshStart(fn func(provider, profile string)) func(*MonitorConfig) {
	return func(c *MonitorConfig) {
		c.OnRefreshStart = fn
	}
}

// WithOnRefreshComplete sets the refresh complete callback.
func WithOnRefreshComplete(fn func(provider, profile string, newExpiry time.Time, err error)) func(*MonitorConfig) {
	return func(c *MonitorConfig) {
		c.OnRefreshComplete = fn
	}
}
