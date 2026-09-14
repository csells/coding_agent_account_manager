package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/exec"
	"github.com/spf13/cobra"
)

var resumeCmd = &cobra.Command{
	Use:   "resume <agent> <profile> [prompt...]",
	Short: "Resume a Codex session for a profile",
	Long: `Resumes a Codex chat session using an isolated profile.

By default, this uses the most recent session ID captured from 'caam exec'.
Use --session to resume a specific session ID.

Examples:
  caam resume codex work
  caam resume codex work --session 019b2e3d-b524-7c22-91da-47de9068d09a
  caam resume codex work "proceed"`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		name := args[1]

		if tool != "codex" {
			return fmt.Errorf("resume is currently only supported for codex")
		}

		prov, ok := registry.Get(tool)
		if !ok {
			return fmt.Errorf("unknown agent: %s", tool)
		}

		prof, err := profileStore.Load(tool, name)
		if err != nil {
			return err
		}

		sessionFlag, _ := cmd.Flags().GetString("session")
		sessionID := sessionFlag
		if sessionID == "" {
			sessionID = prof.LastSessionID
		}
		if sessionID == "" {
			return fmt.Errorf("no session to resume. Start one with: caam exec %s %s", tool, name)
		}

		toolArgs := []string{"resume", sessionID}
		if len(args) > 2 {
			toolArgs = append(toolArgs, args[2:]...)
		}

		noLock, _ := cmd.Flags().GetBool("no-lock")
		ctx := context.Background()
		return runner.Run(ctx, exec.RunOptions{
			Profile:  prof,
			Provider: prov,
			Args:     toolArgs,
			NoLock:   noLock,
		})
	},
}

func init() {
	rootCmd.AddCommand(resumeCmd)
	resumeCmd.Flags().StringP("session", "s", "", "session ID to resume (defaults to last captured)")
	resumeCmd.Flags().Bool("no-lock", false, "don't lock the profile during execution")
}
