package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	codexprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/switcher"
)

// execCommand allows mocking exec.CommandContext in tests
var execCommand = exec.CommandContext

var addCmd = &cobra.Command{
	Use:   "add <agent> [profile-name]",
	Short: "Add a new account with one command",
	Long: `Add a new account by running the agent's login — the command-line
form of the dashboard's n key. A login is a logout first, so the order
matters:
  1. Captures the signed-in account into the vault (its newest tokens),
     or files an unknown live credential as a backup
  2. Clears the live credential, so the agent's login has nothing to revoke
  3. Runs the agent's login and waits for you to complete it
  4. Files the new session under the account that signed in (or asks for
     a name when the agent's credential names nobody)

The account that just logged in is the live one; there is nothing to
activate. --no-activate is accepted for compatibility and does nothing.

Examples:
  caam add claude              # Filed under the account that signs in
  caam add claude work-2       # Name to use if the credential names nobody
  caam add codex --device-code # Device code flow (headless)
  caam add gemini --timeout 5m # Custom timeout for login flow`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runAdd,
}

func init() {
	rootCmd.AddCommand(addCmd)
	addCmd.Flags().Bool("no-activate", false, "don't activate the new profile after adding")
	addCmd.Flags().Duration("timeout", 5*time.Minute, "timeout for login flow completion")
	addCmd.Flags().Bool("force", false, "skip confirmation prompts")
	addCmd.Flags().Bool("device-code", false, "use device code flow for codex (headless)")
}

func runAdd(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	noActivate, _ := cmd.Flags().GetBool("no-activate")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	force, _ := cmd.Flags().GetBool("force")
	deviceCode, _ := cmd.Flags().GetBool("device-code")

	getFileSet, ok := tools[tool]
	if !ok {
		return fmt.Errorf("unknown agent: %s (supported: %s)", tool, supportedToolsList())
	}

	// Initialize vault
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	fileSet := getFileSet()

	// Determine profile name
	var profileName string
	if len(args) == 2 {
		profileName = args[1]
	}

	// Check if profile name already exists
	if profileName != "" {
		profiles, err := vault.List(tool)
		if err == nil {
			for _, p := range profiles {
				if p == profileName {
					return fmt.Errorf("profile %s/%s already exists (use a different name or delete it first)", tool, profileName)
				}
			}
		}
	}

	// Check if auth files currently exist
	hasExistingAuth := authfile.HasAuthFiles(fileSet)

	if hasExistingAuth && !force {
		fmt.Printf("Current %s auth will be backed up and cleared.\n", tool)
		ok, err := confirmProceed(cmd.InOrStdin(), cmd.OutOrStdout())
		if err != nil {
			return fmt.Errorf("confirm proceed: %w", err)
		}
		if !ok {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// The shared login sequence (internal/switcher.Login, the same as the
	// dashboard's n): vault the signed-in account with its newest tokens,
	// clear its live credential so the tool's login has nothing to
	// revoke, run the login, file the new session under the account that
	// signed in.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var loginDB *caamdb.DB
	if spm, err := config.LoadSPMConfig(); err == nil && spm.Analytics.Enabled {
		loginDB, _ = getDB()
	}
	res, err := switcher.Login(ctx, vault, fileSet, switcher.LoginOptions{
		DB: loginDB,
		Run: func(ctx context.Context) error {
			fmt.Printf("\nLaunching %s login...\n", tool)
			fmt.Println("Complete the authentication in the terminal/browser.")
			fmt.Println("Press Ctrl+C when done or if you want to cancel.")
			fmt.Println()
			return runToolLoginInterruptible(ctx, tool, deviceCode, timeout)
		},
		Identity: func(ctx context.Context) string { return liveAccountIdentity(ctx, tool) },
		Name:     profileName,
	})
	if err != nil {
		return err
	}
	if res.Previous != "" {
		fmt.Printf("Captured the signed-in account as %s/%s before the login.\n", tool, res.Previous)
	}
	if !authfile.HasAuthFiles(fileSet) {
		fmt.Println("\nNo auth files detected after login.")
		return fmt.Errorf("login did not create auth files")
	}
	fmt.Println("\nLogin successful!")

	// The tool's credential names nobody: ask.
	profileName = res.Account
	if res.NeedsName {
		fmt.Print("Profile name: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		profileName = strings.TrimSpace(input)
		if profileName == "" {
			profileName = "new-account"
		}
		if strings.HasPrefix(profileName, "_") {
			return fmt.Errorf("profile names starting with '_' are reserved for system use")
		}
		profiles, _ := vault.List(tool)
		for _, p := range profiles {
			if p == profileName {
				profileName = fmt.Sprintf("%s_%s", profileName, time.Now().Format("150405"))
				fmt.Printf("Profile name already exists, using: %s\n", profileName)
				break
			}
		}
		if _, err := switcher.FinishLogin(ctx, vault, fileSet, nil, switcher.LoginOptions{Name: profileName, DB: loginDB}); err != nil {
			return fmt.Errorf("save profile: %w", err)
		}
	}
	fmt.Printf("  Saved %s/%s\n", tool, profileName)
	// The account that just logged in is the live one; nothing to activate.
	_ = noActivate

	fmt.Println()
	fmt.Println("Done! Your new account has been added.")
	fmt.Printf("\nQuick commands:\n")
	fmt.Printf("  caam activate %s %s  # Switch to this profile\n", tool, profileName)
	fmt.Printf("  caam ls %s            # List all %s profiles\n", tool, tool)

	return nil
}

// runToolLoginInterruptible runs the tool's login, stopping on ctrl-c or
// the timeout.
func runToolLoginInterruptible(ctx context.Context, tool string, deviceCode bool, timeout time.Duration) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	done := make(chan error, 1)
	go func() { done <- runToolLogin(ctx, tool, deviceCode) }()
	select {
	case err := <-done:
		return err
	case <-sigChan:
		fmt.Println("\n\nLogin interrupted.")
		return fmt.Errorf("login interrupted")
	case <-ctx.Done():
		fmt.Printf("\nTimeout after %v waiting for login to complete.\n", timeout)
		return fmt.Errorf("login timed out; retry with 'caam add %s' or use --timeout to increase wait time", tool)
	}
}

// runToolLogin runs the provider's own login (the same command the
// dashboard's n key runs) in this terminal. Codex is first pointed at the
// file credential store so the login lands where caam can capture it.
func runToolLogin(ctx context.Context, tool string, deviceCode bool) error {
	login, err := loginCommandFor(tool, deviceCode)
	if err != nil {
		return err
	}
	if tool == "codex" {
		if err := codexprovider.EnsureFileCredentialStore(codexprovider.ResolveHome()); err != nil {
			return fmt.Errorf("configure codex credential store: %w", err)
		}
	}
	cmd := execCommand(ctx, login.Bin, login.Args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
