package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/handoff"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/notify"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pty"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/ratelimit"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/switcher"
	"golang.org/x/term"
)

// ExecCommand allows mocking exec.CommandContext in tests
var ExecCommand = exec.CommandContext

// SmartRunner orchestrates the auto-handoff flow for seamless profile switching.
// When a rate limit is detected in the CLI output, SmartRunner:
// 1. Selects the best backup profile using the rotation algorithm
// 2. Swaps auth files atomically
// 3. Injects the login command via PTY
// 4. Waits for login completion
// 5. Notifies the user and continues execution
//
// On any failure, it rolls back to the original profile and shows manual instructions.
type SmartRunner struct {
	*Runner

	detector      *ratelimit.Detector
	rotation      *rotation.Selector
	vault         *authfile.Vault
	db            *caamdb.DB
	authPool      *authpool.AuthPool
	ptyController pty.Controller
	loginHandler  handoff.LoginHandler
	notifier      notify.Notifier

	// Cooldown duration to apply when rate limit is detected
	cooldownDuration time.Duration

	// State (protected by mu)
	mu              sync.Mutex
	currentProfile  string
	previousProfile string // For rollback
	handoffCount    int
	// restartArgs/restartPending: a handoff asked Run to end the session and
	// respawn it on its history with these args.
	restartArgs    []string
	restartPending bool
	state          HandoffState

	// WaitGroup to track background goroutines (handleRateLimit)
	wg sync.WaitGroup

	// Login detection channel
}

// SmartRunnerOptions configures the SmartRunner.
type SmartRunnerOptions struct {
	Notifier         notify.Notifier
	Vault            *authfile.Vault
	DB               *caamdb.DB
	AuthPool         *authpool.AuthPool
	Rotation         *rotation.Selector
	CooldownDuration time.Duration
}

// NewSmartRunner creates a new SmartRunner.
func NewSmartRunner(runner *Runner, opts SmartRunnerOptions) *SmartRunner {
	// Use default notifier if none provided
	notifier := opts.Notifier
	if notifier == nil {
		notifier = &notify.TerminalNotifier{}
	}

	return &SmartRunner{
		Runner:           runner,
		vault:            opts.Vault,
		db:               opts.DB,
		authPool:         opts.AuthPool,
		rotation:         opts.Rotation,
		notifier:         notifier,
		cooldownDuration: opts.CooldownDuration,
		state:            Running,
	}
}

// Run executes the command with smart handoff capabilities.
func (r *SmartRunner) Run(ctx context.Context, opts RunOptions) (err error) {
	// Claude Code is a full TUI application that manages its own terminal.
	// The nested PTY wrapper conflicts with its terminal handling, causing hangs.
	// Bypass SmartRunner entirely but still log the session for analytics.
	if opts.Provider.ID() == "claude" {
		r.currentProfile = opts.Profile.Name
		if r.db != nil {
			_ = r.db.Log(caamdb.Event{
				Type:        caamdb.EventActivate,
				Provider:    opts.Provider.ID(),
				ProfileName: r.currentProfile,
				Timestamp:   time.Now(),
			})
			startTime := time.Now()
			defer func() {
				duration := time.Since(startTime)
				finalCode := 0
				if err != nil {
					var exitErr *ExitCodeError
					if errors.As(err, &exitErr) {
						finalCode = exitErr.Code
					} else {
						finalCode = 1
					}
				}
				_ = r.db.RecordWrapSession(caamdb.WrapSession{
					Provider:        opts.Provider.ID(),
					ProfileName:     r.currentProfile,
					StartedAt:       startTime,
					EndedAt:         time.Now(),
					DurationSeconds: int(duration.Seconds()),
					ExitCode:        finalCode,
				})
			}()
		}
		return r.Runner.Run(ctx, opts)
	}

	// Initialize rate limit detector
	detector, err := ratelimit.NewDetector(
		ratelimit.ProviderFromString(opts.Provider.ID()),
		nil, // Use default patterns
	)
	if err != nil {
		return fmt.Errorf("create detector: %w", err)
	}
	r.detector = detector

	// Get login handler
	r.loginHandler = handoff.GetHandler(opts.Provider.ID())
	if r.loginHandler == nil {
		// Fallback to basic runner if no login handler (can't do handoff)
		return r.Runner.Run(ctx, opts)
	}

	r.currentProfile = opts.Profile.Name

	// Log activation event
	if r.db != nil {
		_ = r.db.Log(caamdb.Event{
			Type:        caamdb.EventActivate,
			Provider:    opts.Provider.ID(),
			ProfileName: r.currentProfile,
			Timestamp:   time.Now(),
		})
	}

	// Track session
	startTime := time.Now()
	defer func() {
		if r.db != nil {
			duration := time.Since(startTime)
			// Determine final exit code from error
			finalCode := 0
			if err != nil {
				var exitErr *ExitCodeError
				// Check if it's an ExitCodeError (wrapper type in this package)
				if errors.As(err, &exitErr) {
					finalCode = exitErr.Code
				} else {
					finalCode = 1 // Generic error
				}
			}

			session := caamdb.WrapSession{
				Provider:        opts.Provider.ID(),
				ProfileName:     r.currentProfile, // Use the final profile
				StartedAt:       startTime,
				EndedAt:         time.Now(),
				DurationSeconds: int(duration.Seconds()),
				ExitCode:        finalCode,
				RateLimitHit:    r.handoffCount > 0,
			}
			if r.handoffCount > 0 {
				session.Notes = fmt.Sprintf("handoffs: %d", r.handoffCount)
			}
			_ = r.db.RecordWrapSession(session)
		}
	}()

	// Lock profile
	if !opts.NoLock {
		if err := opts.Profile.LockWithCleanup(); err != nil {
			return fmt.Errorf("lock profile: %w", err)
		}
		defer opts.Profile.Unlock()
	}

	// Get env. Honor UseGlobalEnv exactly like Runner.Run does (issue #64):
	// vault-based runs (`caam run`) swap auth files inside the REAL home, so
	// injecting the provider's isolated-profile env (HOME, CODEX_HOME, ...)
	// would point the tool at a profile directory that is not logged in.
	var providerEnv map[string]string
	if !opts.UseGlobalEnv {
		providerEnv, err = opts.Provider.Env(ctx, opts.Profile)
		if err != nil {
			return fmt.Errorf("get provider env: %w", err)
		}
	}

	// Build the spawn once; a resumed session reuses it with resume args.
	envMap := make(map[string]string)
	for _, e := range os.Environ() {
		parts := splitEnv(e)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	for k, v := range providerEnv {
		envMap[k] = v
	}
	for k, v := range opts.Env {
		envMap[k] = v
	}
	spawn := childSpawn{bin: opts.Provider.DefaultBin(), workDir: opts.WorkDir}
	for k, v := range envMap {
		spawn.env = append(spawn.env, k+"="+v)
	}
	var capture *codexSessionCapture
	if opts.Provider.ID() == "codex" {
		capture = &codexSessionCapture{}
	}

	// Run the session; when a handoff switched the account under it, the
	// session is ended and respawned on its own history (the tool's resume
	// flags), so the new credential is in use at once.
	args := opts.Args
	var exitCode int
	var waitErr error
	for {
		exitCode, waitErr = r.runChild(ctx, spawn, args, capture)
		if resume, ok := r.takeRestart(); ok {
			args = resume
			continue
		}
		break
	}

	// Update profile metadata
	now := time.Now()
	opts.Profile.LastUsedAt = now
	if capture != nil {
		if sessionID := capture.ID(); sessionID != "" {
			opts.Profile.LastSessionID = sessionID
			opts.Profile.LastSessionTS = now.UTC()
		}
	}
	if saveErr := opts.Profile.Save(); saveErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save profile metadata: %v\n", saveErr)
	}

	if waitErr != nil {
		return fmt.Errorf("command failed: %w", waitErr)
	}
	if exitCode != 0 {
		return &ExitCodeError{Code: exitCode}
	}

	return nil
}

// childSpawn is what does not change between a session and its resumed
// successor: the binary, its environment and working directory.
type childSpawn struct {
	bin     string
	env     []string
	workDir string
}

// runChild spawns the tool under a PTY with args, proxies the terminal,
// monitors its output for a rate limit, and returns its exit code once it
// is gone and every handoff goroutine has finished.
func (r *SmartRunner) runChild(ctx context.Context, spawn childSpawn, args []string, capture *codexSessionCapture) (int, error) {
	cmd := ExecCommand(ctx, spawn.bin, args...)
	cmd.Env = spawn.env
	if spawn.workDir != "" {
		cmd.Dir = spawn.workDir
	}

	// Terminal proxying (issue #74): when stdin is a real terminal, the child's
	// PTY is created at that terminal's size and follows it, the terminal is
	// switched to raw mode so keystrokes reach the child un-echoed and
	// un-interpreted, and stdin is relayed into the PTY. See terminal_proxy.go.
	stdinFd := int(os.Stdin.Fd())
	interactive := term.IsTerminal(stdinFd)
	ptyOpts := pty.DefaultOptions()
	if interactive {
		if rows, cols, ok := terminalSize(stdinFd); ok {
			ptyOpts.Rows, ptyOpts.Cols = rows, cols
		}
	}

	// Create PTY controller
	ctrl, err := pty.NewController(cmd, ptyOpts)
	if err != nil {
		return 0, fmt.Errorf("create pty controller: %w", err)
	}
	r.ptyController = ctrl
	defer ctrl.Close()

	// Start the PTY (this executes the command)
	if err := ctrl.Start(); err != nil {
		return 0, fmt.Errorf("start pty: %w", err)
	}

	// restoreTerminal puts the real terminal back into its pre-run state. It
	// is called explicitly as soon as the child has exited (before anything
	// else is printed) and deferred as a safety net for early returns.
	restoreTerminal := func() {}
	if interactive {
		if state, rawErr := term.MakeRaw(stdinFd); rawErr == nil {
			var once sync.Once
			restoreTerminal = func() {
				once.Do(func() { _ = term.Restore(stdinFd, state) })
			}
		}
		stopResize := watchTerminalResize(stdinFd, ctrl)
		defer stopResize()
	}
	defer restoreTerminal()

	// Relay input into the child's PTY. This runs for pipes as well as
	// terminals so `echo prompt | caam run ...` reaches the tool; the goroutine
	// ends when stdin is exhausted or the PTY closes.
	go relayStdin(os.Stdin, ctrl)

	// Start output monitoring in background
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()
	monitorDone := make(chan struct{})

	var observer func(string)
	if capture != nil {
		observer = capture.ObserveLine
	}
	go r.monitorOutput(monitorCtx, ctrl, monitorDone, observer)

	// Wait for command completion using the controller's Wait method
	exitCode, waitErr := ctrl.Wait()

	// Cancel monitor context, wait for monitor to stop, then wait for any handoff goroutines.
	cancelMonitor()
	<-monitorDone
	r.wg.Wait()

	// The child is gone and its output fully drained: give the terminal back
	// before any further (cooked-mode) output such as warnings below.
	restoreTerminal()
	return exitCode, waitErr
}

// requestRestart asks Run to end the current session and respawn it with
// args once it has exited.
func (r *SmartRunner) requestRestart(args []string) {
	r.mu.Lock()
	r.restartArgs = args
	r.restartPending = true
	ctrl := r.ptyController
	r.mu.Unlock()
	if ctrl != nil {
		_ = ctrl.Signal(pty.SIGTERM)
	}
}

// takeRestart returns the pending resume args, once.
func (r *SmartRunner) takeRestart() ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.restartPending {
		return nil, false
	}
	r.restartPending = false
	return r.restartArgs, true
}

// handleRateLimit handles the rate limit detection and handoff flow.
func (r *SmartRunner) handleRateLimit(ctx context.Context) {
	r.mu.Lock()
	if r.state != Running {
		r.mu.Unlock()
		return // Already handling or failed
	}
	r.state = RateLimited
	r.mu.Unlock()

	// Notify detection
	r.notifyHandoff(r.currentProfile, "selecting backup...")

	// Get file set
	fileSet, ok := authfile.GetAuthFileSet(r.loginHandler.Provider())
	if !ok {
		r.failWithManual("unknown provider file set")
		return
	}

	// 1. Remember where we came from; the switch below captures it.
	r.previousProfile = r.currentProfile

	defer func() {
		if r.getState() == HandoffFailed {
			r.rollback(fileSet)
		}
	}()

	// 2. Select best backup profile
	r.setState(SelectingBackup)

	// Get all profiles
	profiles, err := r.vault.List(r.loginHandler.Provider())
	if err != nil {
		r.failWithManual("failed to list profiles: %v", err)
		return
	}

	// Select best
	selection, err := r.rotation.Select(r.loginHandler.Provider(), profiles, r.currentProfile)
	if err != nil {
		r.failWithManual("no backup available: %v", err)
		return
	}
	nextProfile := selection.Selected

	if nextProfile == r.currentProfile {
		r.failWithManual("no other profiles available")
		return
	}

	r.notifyHandoff(r.currentProfile, nextProfile)

	// 3. Mark current profile as in cooldown (if authPool is available)
	cooldownDuration := r.cooldownDuration
	if cooldownDuration == 0 {
		cooldownDuration = 60 * time.Minute
	}
	if r.authPool != nil {
		r.authPool.SetCooldown(r.loginHandler.Provider(), r.currentProfile, cooldownDuration)
	}
	if r.db != nil {
		r.db.SetCooldown(r.loginHandler.Provider(), r.currentProfile, time.Now(), cooldownDuration, "auto-detected via SmartRunner")
	}

	// 4. Switch through the shared core: the outgoing profile is
	// re-captured first (a failure refuses the switch — the vault must never
	// be left with a stale copy of a rotating family), then the next
	// profile's credential is installed. The core's own refresh gate and
	// safety config apply.
	r.setState(SwappingAuth)
	if _, err := switcher.Switch(ctx, r.vault, fileSet, switcher.Options{Profile: nextProfile, DB: r.db, Source: "handoff"}); err != nil {
		r.failWithManual("auth swap failed: %v", err)
		return
	}

	// 5. No login: the restored credential is a session already, and a
	// tool's login is a logout first — it would revoke what was just
	// installed (docs/ACCOUNT_SWITCHER.md §2). The running session holds
	// its credential in memory, so it is ended and respawned on its own
	// history with the new one.
	r.setState(Switched)
	r.currentProfile = nextProfile
	r.handoffCount++

	r.notifier.Notify(&notify.Alert{
		Level:   notify.Info,
		Title:   "Profile switched",
		Message: fmt.Sprintf("Switched to %s and resumed the session on its history.", nextProfile),
	})
	r.requestRestart(r.loginHandler.ResumeArgs())

	// Reset detector state so we don't immediately trigger again
	r.detector.Reset()
	r.setState(Running)
}

func (r *SmartRunner) rollback(fileSet authfile.AuthFileSet) {
	fmt.Fprintf(os.Stderr, "Rolling back to %s...\n", r.previousProfile)
	if r.previousProfile == "" {
		return
	}
	if _, err := switcher.Switch(context.Background(), r.vault, fileSet, switcher.Options{Profile: r.previousProfile, DB: r.db, Source: "handoff-rollback"}); err != nil {
		fmt.Fprintf(os.Stderr, "Rollback failed: %v\n", err)
	}
	r.currentProfile = r.previousProfile
	r.detector.Reset()
	r.setState(Running)
}

func (r *SmartRunner) failWithManual(format string, args ...interface{}) {
	r.setState(HandoffFailed)
	msg := fmt.Sprintf(format, args...)

	r.notifier.Notify(&notify.Alert{
		Level:   notify.Warning,
		Title:   "Auto-handoff failed",
		Message: msg,
		Action:  "Run 'caam ls' to see available profiles, then 'caam activate <profile>'",
	})

	fmt.Fprintf(os.Stderr, "\n[caam] Auto-handoff failed: %s\n", msg)
}

func (r *SmartRunner) notifyHandoff(from, to string, msg ...string) {
	message := fmt.Sprintf("Rate limit on %s, switching to %s...", from, to)
	if len(msg) > 0 {
		message = msg[0]
	}
	r.notifier.Notify(&notify.Alert{
		Level:   notify.Info,
		Title:   "Switching profiles",
		Message: message,
	})
}

func (r *SmartRunner) setState(s HandoffState) {
	r.mu.Lock()
	r.state = s
	r.mu.Unlock()
}

func (r *SmartRunner) getState() HandoffState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

func (r *SmartRunner) monitorOutput(ctx context.Context, ctrl pty.Controller, done chan<- struct{}, observer func(string)) {
	defer close(done)
	// Create an observing writer to handle split packets and buffering
	// Use a local flag to prevent repeated dispatching within this loop context
	dispatched := false

	writer := ratelimit.NewObservingWriter(r.detector, func(line string) {
		if observer != nil {
			observer(line)
		}
		// This callback is triggered when a complete line is processed
		if !dispatched && r.detector.Detected() {
			dispatched = true
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				r.handleRateLimit(ctx)
			}()
		}
	})
	defer writer.Flush()

	// draining indicates context was cancelled and we're draining remaining PTY output
	draining := false

	for {
		// Poll for output (ReadOutput is non-blocking with timeout)
		output, err := ctrl.ReadOutput()
		if err != nil {
			// EOF or error - PTY closed, stop reading
			break
		}

		if output != "" {
			os.Stdout.Write([]byte(output))

			r.mu.Lock()
			state := r.state
			r.mu.Unlock()

			if state == Running {
				// If detector was reset (e.g. after successful handoff), allow new dispatch
				if !r.detector.Detected() {
					dispatched = false
				}

				// Only write to observer if we haven't dispatched yet
				// This avoids processing output during the handoff transition
				if !dispatched {
					writer.Write([]byte(output))
				}
			}

		}

		// Check context cancellation
		if !draining {
			select {
			case <-ctx.Done():
				// Context cancelled, but continue draining PTY buffer until EOF
				// Set a deadline to prevent infinite draining if process doesn't exit
				draining = true
				go func() {
					time.Sleep(5 * time.Second)
					// Force close PTY if still draining after timeout
					ctrl.Close()
				}()
			case <-time.After(10 * time.Millisecond):
				// Yield
			}
		}
		// In drain mode, continue looping without delay until ReadOutput returns EOF
	}
}

func splitEnv(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}
