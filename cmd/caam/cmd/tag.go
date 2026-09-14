package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

var tagCmd = &cobra.Command{
	Use:   "tag",
	Short: "Manage profile tags",
	Long: `Manage tags for profile categorization.

Tags are unordered labels for organizing profiles into categories.
Unlike favorites (which are ordered), tags let you group profiles
by project, team, environment, client, etc.

Tag format: lowercase letters, numbers, and hyphens only (max 32 chars).
Each profile can have up to 10 tags.

Examples:
  caam tag add claude work project-x    # Add tags to a profile
  caam tag remove claude work personal  # Remove a tag from a profile
  caam tag list claude work             # List tags for a profile
  caam tag clear claude work            # Remove all tags from a profile
  caam tag all claude                   # List all tags used for an agent`,
}

var tagAddCmd = &cobra.Command{
	Use:   "add <agent> <profile> <tag> [tag...]",
	Short: "Add tags to a profile",
	Long: `Add one or more tags to a profile.

Tags must be lowercase, alphanumeric with hyphens only, max 32 characters.
Each profile can have up to 10 tags.

Examples:
  caam tag add claude work project-x
  caam tag add codex main testing dev`,
	Args: cobra.MinimumNArgs(3),
	RunE: runTagAdd,
}

var tagRemoveCmd = &cobra.Command{
	Use:   "remove <agent> <profile> <tag> [tag...]",
	Short: "Remove tags from a profile",
	Long: `Remove one or more tags from a profile.

Examples:
  caam tag remove claude work project-x
  caam tag remove codex main testing`,
	Args: cobra.MinimumNArgs(3),
	RunE: runTagRemove,
}

var tagListCmd = &cobra.Command{
	Use:   "list <agent> <profile>",
	Short: "List tags for a profile",
	Long: `List all tags assigned to a profile.

Examples:
  caam tag list claude work
  caam tag list codex main --json`,
	Args: cobra.ExactArgs(2),
	RunE: runTagList,
}

var tagClearCmd = &cobra.Command{
	Use:   "clear <agent> <profile>",
	Short: "Remove all tags from a profile",
	Long: `Remove all tags from a profile.

Examples:
  caam tag clear claude work`,
	Args: cobra.ExactArgs(2),
	RunE: runTagClear,
}

var tagAllCmd = &cobra.Command{
	Use:   "all <agent>",
	Short: "List all tags used for an agent",
	Long: `List all unique tags used across all profiles for an agent.

Examples:
  caam tag all claude
  caam tag all codex --json`,
	Args: cobra.ExactArgs(1),
	RunE: runTagAll,
}

func init() {
	rootCmd.AddCommand(tagCmd)
	tagCmd.AddCommand(tagAddCmd)
	tagCmd.AddCommand(tagRemoveCmd)
	tagCmd.AddCommand(tagListCmd)
	tagCmd.AddCommand(tagClearCmd)
	tagCmd.AddCommand(tagAllCmd)

	// Add --json flag to list and all commands
	tagListCmd.Flags().Bool("json", false, "output in JSON format")
	tagAllCmd.Flags().Bool("json", false, "output in JSON format")
}

func runTagAdd(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	profileName := args[1]
	tags := args[2:]

	if profileStore == nil {
		profileStore = profile.NewStore(profile.DefaultStorePath())
	}

	// Load profile
	prof, err := profileStore.Load(tool, profileName)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}

	// Add each tag
	added := 0
	for _, tag := range tags {
		normalized := profile.NormalizeTag(tag)
		if err := prof.AddTag(normalized); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot add tag %q: %v\n", tag, err)
			continue
		}
		added++
	}

	// Save profile
	if err := prof.Save(); err != nil {
		return fmt.Errorf("save profile: %w", err)
	}

	if added == 1 {
		fmt.Printf("Added 1 tag to %s/%s\n", tool, profileName)
	} else {
		fmt.Printf("Added %d tags to %s/%s\n", added, tool, profileName)
	}
	fmt.Printf("Tags: %s\n", strings.Join(prof.Tags, ", "))

	return nil
}

func runTagRemove(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	profileName := args[1]
	tags := args[2:]

	if profileStore == nil {
		profileStore = profile.NewStore(profile.DefaultStorePath())
	}

	// Load profile
	prof, err := profileStore.Load(tool, profileName)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}

	// Remove each tag
	removed := 0
	for _, tag := range tags {
		if prof.RemoveTag(tag) {
			removed++
		} else {
			fmt.Fprintf(os.Stderr, "Warning: tag %q not found on profile\n", tag)
		}
	}

	// Save profile
	if err := prof.Save(); err != nil {
		return fmt.Errorf("save profile: %w", err)
	}

	if removed == 1 {
		fmt.Printf("Removed 1 tag from %s/%s\n", tool, profileName)
	} else {
		fmt.Printf("Removed %d tags from %s/%s\n", removed, tool, profileName)
	}

	if len(prof.Tags) > 0 {
		fmt.Printf("Remaining tags: %s\n", strings.Join(prof.Tags, ", "))
	} else {
		fmt.Println("No tags remaining")
	}

	return nil
}

func runTagList(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	profileName := args[1]
	jsonOutput, _ := cmd.Flags().GetBool("json")

	if profileStore == nil {
		profileStore = profile.NewStore(profile.DefaultStorePath())
	}

	// Load profile
	prof, err := profileStore.Load(tool, profileName)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}

	if jsonOutput {
		output := struct {
			Tool    string   `json:"tool"`
			Profile string   `json:"profile"`
			Tags    []string `json:"tags"`
		}{
			Tool:    tool,
			Profile: profileName,
			Tags:    prof.Tags,
		}
		if output.Tags == nil {
			output.Tags = []string{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	if len(prof.Tags) == 0 {
		fmt.Printf("No tags for %s/%s\n", tool, profileName)
		return nil
	}

	fmt.Printf("Tags for %s/%s:\n", tool, profileName)
	for _, tag := range prof.Tags {
		fmt.Printf("  %s\n", tag)
	}

	return nil
}

func runTagClear(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	profileName := args[1]

	if profileStore == nil {
		profileStore = profile.NewStore(profile.DefaultStorePath())
	}

	// Load profile
	prof, err := profileStore.Load(tool, profileName)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}

	count := len(prof.Tags)
	prof.ClearTags()

	// Save profile
	if err := prof.Save(); err != nil {
		return fmt.Errorf("save profile: %w", err)
	}

	switch count {
	case 0:
		fmt.Printf("No tags to clear for %s/%s\n", tool, profileName)
	case 1:
		fmt.Printf("Cleared 1 tag from %s/%s\n", tool, profileName)
	default:
		fmt.Printf("Cleared %d tags from %s/%s\n", count, tool, profileName)
	}

	return nil
}

func runTagAll(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	jsonOutput, _ := cmd.Flags().GetBool("json")

	if profileStore == nil {
		profileStore = profile.NewStore(profile.DefaultStorePath())
	}

	// Get all tags
	tags, err := profileStore.AllTags(tool)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}

	// Sort tags alphabetically
	sort.Strings(tags)

	if jsonOutput {
		output := struct {
			Tool string   `json:"tool"`
			Tags []string `json:"tags"`
		}{
			Tool: tool,
			Tags: tags,
		}
		if output.Tags == nil {
			output.Tags = []string{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	if len(tags) == 0 {
		fmt.Printf("No tags used for %s profiles\n", tool)
		return nil
	}

	fmt.Printf("Tags used for %s profiles:\n", tool)
	for _, tag := range tags {
		fmt.Printf("  %s\n", tag)
	}

	return nil
}
