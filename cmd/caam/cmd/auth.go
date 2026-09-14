// Package cmd implements the CLI commands for caam.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/bundle"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// AuthDetectResult represents the result of auth detection for one provider.
type AuthDetectResult struct {
	Provider  string               `json:"provider"`
	Found     bool                 `json:"found"`
	Locations []AuthDetectLocation `json:"locations"`
	Primary   *AuthDetectLocation  `json:"primary,omitempty"`
	Warning   string               `json:"warning,omitempty"`
	Error     string               `json:"error,omitempty"`
}

// AuthDetectLocation represents a detected auth file location.
type AuthDetectLocation struct {
	Path            string `json:"path"`
	Exists          bool   `json:"exists"`
	LastModified    string `json:"last_modified,omitempty"`
	FileSize        int64  `json:"file_size,omitempty"`
	IsValid         bool   `json:"is_valid"`
	ValidationError string `json:"validation_error,omitempty"`
	Description     string `json:"description"`
}

// AuthDetectReport contains the results of auth detection for all providers.
type AuthDetectReport struct {
	Timestamp string             `json:"timestamp"`
	Results   []AuthDetectResult `json:"results"`
	Summary   AuthDetectSummary  `json:"summary"`
}

// AuthDetectSummary provides a summary of detected auth.
type AuthDetectSummary struct {
	TotalProviders int `json:"total_providers"`
	FoundCount     int `json:"found_count"`
	NotFoundCount  int `json:"not_found_count"`
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication credentials",
	Long: `Commands for managing authentication credentials.

Subcommands:
  detect  - Detect existing auth files in system locations
  import  - Import detected auth into a caam profile`,
}

var authDetectCmd = &cobra.Command{
	Use:   "detect [agent]",
	Short: "Detect existing auth files",
	Long: `Detect existing authentication files in standard system locations.

This scans for existing auth files from direct agent usage:
  - Claude: ~/.claude.json, ~/.config/claude-code/auth.json, $CLAUDE_CONFIG_DIR/auth.json
  - Codex: ~/.codex/auth.json
  - Gemini: ~/.gemini/settings.json, ~/.gemini/.env, gcloud ADC

If an agent argument is provided, only that agent is checked.
Otherwise, all supported agents are scanned.

Examples:
  caam auth detect           # Detect all agents
  caam auth detect claude    # Detect Claude auth only
  caam auth detect --json    # Output as JSON

This is useful for first-run experience to discover and import existing credentials.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")

		var providersToCheck []provider.Provider

		if len(args) > 0 {
			// Check specific provider
			tool := strings.ToLower(args[0])
			p, ok := registry.Get(tool)
			if !ok {
				return fmt.Errorf("unknown agent: %s (supported: claude, codex, gemini)", tool)
			}
			providersToCheck = append(providersToCheck, p)
		} else {
			// Check all providers
			providersToCheck = registry.All()
		}

		report := runAuthDetection(providersToCheck)

		if jsonOutput {
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		printAuthDetectReport(report)
		return nil
	},
}

// AuthImportResult represents the result of an auth import operation.
type AuthImportResult struct {
	Provider    string   `json:"provider"`
	ProfileName string   `json:"profile_name"`
	ProfilePath string   `json:"profile_path"`
	SourceFile  string   `json:"source_file"`
	CopiedFiles []string `json:"copied_files"`
	Success     bool     `json:"success"`
	Error       string   `json:"error,omitempty"`
}

var authImportCmd = &cobra.Command{
	Use:   "import <agent>",
	Short: "Import detected auth into a profile",
	Long: `Import existing authentication files into a new caam profile.

This detects existing auth credentials and imports them into a new profile,
allowing you to manage multiple accounts without re-authenticating.

The agent argument is required and specifies which agent:
  - claude  - Claude Code (Anthropic)
  - codex   - Codex CLI (OpenAI)
  - gemini  - Gemini CLI (Google)

Examples:
  caam auth import claude                    # Import Claude auth to 'default' profile
  caam auth import codex -n work             # Import Codex auth to 'work' profile
  caam auth import gemini --source ~/.gemini/settings.json  # Import specific file
  caam auth import claude --force            # Overwrite existing profile
  caam auth import claude --json             # Output as JSON

Use 'caam auth detect' first to see what auth files are available.`,
	Args: cobra.ExactArgs(1),
	RunE: runAuthImport,
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authDetectCmd)
	authDetectCmd.Flags().Bool("json", false, "output in JSON format")

	authCmd.AddCommand(authImportCmd)
	authImportCmd.Flags().StringP("name", "n", "default", "profile name")
	authImportCmd.Flags().StringP("description", "d", "", "profile description")
	authImportCmd.Flags().Bool("force", false, "overwrite existing profile")
	authImportCmd.Flags().String("source", "", "path to auth file (overrides detection)")
	authImportCmd.Flags().Bool("json", false, "output in JSON format")
}

func runAuthDetection(providers []provider.Provider) *AuthDetectReport {
	report := &AuthDetectReport{
		Timestamp: time.Now().Format(time.RFC3339),
		Results:   make([]AuthDetectResult, 0, len(providers)),
	}

	for _, p := range providers {
		result := AuthDetectResult{
			Provider:  p.ID(),
			Locations: []AuthDetectLocation{},
		}

		detection, err := p.DetectExistingAuth()
		if err != nil {
			result.Error = err.Error()
			report.Results = append(report.Results, result)
			continue
		}

		result.Found = detection.Found
		result.Warning = detection.Warning

		for _, loc := range detection.Locations {
			detectLoc := AuthDetectLocation{
				Path:            loc.Path,
				Exists:          loc.Exists,
				FileSize:        loc.FileSize,
				IsValid:         loc.IsValid,
				ValidationError: loc.ValidationError,
				Description:     loc.Description,
			}
			if !loc.LastModified.IsZero() {
				detectLoc.LastModified = loc.LastModified.Format(time.RFC3339)
			}
			result.Locations = append(result.Locations, detectLoc)
		}

		if detection.Primary != nil {
			primary := AuthDetectLocation{
				Path:            detection.Primary.Path,
				Exists:          detection.Primary.Exists,
				FileSize:        detection.Primary.FileSize,
				IsValid:         detection.Primary.IsValid,
				ValidationError: detection.Primary.ValidationError,
				Description:     detection.Primary.Description,
			}
			if !detection.Primary.LastModified.IsZero() {
				primary.LastModified = detection.Primary.LastModified.Format(time.RFC3339)
			}
			result.Primary = &primary
		}

		report.Results = append(report.Results, result)

		if detection.Found {
			report.Summary.FoundCount++
		} else {
			report.Summary.NotFoundCount++
		}
	}

	report.Summary.TotalProviders = len(providers)

	return report
}

func printAuthDetectReport(report *AuthDetectReport) {
	fmt.Println("Detecting existing auth credentials...")
	fmt.Println()

	for _, result := range report.Results {
		displayName := getProviderDisplayName(result.Provider)
		fmt.Printf("%s:\n", displayName)

		if result.Error != "" {
			fmt.Printf("  ✗ Error: %s\n", result.Error)
			fmt.Println()
			continue
		}

		if !result.Found {
			fmt.Println("  ✗ No existing auth detected")
			// Show checked locations
			if len(result.Locations) > 0 {
				fmt.Println("    Checked:")
				for _, loc := range result.Locations {
					fmt.Printf("    - %s\n", shortenPath(loc.Path))
				}
			}
			fmt.Println()
			continue
		}

		// Show found auth files
		for _, loc := range result.Locations {
			if !loc.Exists {
				continue
			}

			statusIcon := "✓"
			status := "Valid"
			if !loc.IsValid {
				statusIcon = "⚠"
				status = loc.ValidationError
			}

			fmt.Printf("  %s %s\n", statusIcon, shortenPath(loc.Path))
			if loc.LastModified != "" {
				t, err := time.Parse(time.RFC3339, loc.LastModified)
				if err == nil {
					fmt.Printf("    Last modified: %s\n", t.Format("2006-01-02 15:04:05"))
				}
			}
			fmt.Printf("    Size: %s\n", bundle.FormatSize(loc.FileSize))
			fmt.Printf("    Status: %s\n", status)
		}

		if result.Warning != "" {
			fmt.Printf("  ⚠ %s\n", result.Warning)
		}

		fmt.Println()
	}

	// Summary
	fmt.Printf("Summary: %d agent(s) checked, %d with auth, %d without\n",
		report.Summary.TotalProviders,
		report.Summary.FoundCount,
		report.Summary.NotFoundCount)

	if report.Summary.FoundCount > 0 {
		fmt.Println("\nRun 'caam auth import <agent>' to import detected credentials into a profile.")
	}
}

// getProviderDisplayName is the one vocabulary every caam surface prints
// for a provider (provider.Label): the product's name, never the id.
func getProviderDisplayName(id string) string {
	return provider.Label(id)
}

func shortenPath(path string) string {
	// Replace home directory with ~
	homeDir := ""
	if h, err := getHomeDir(); err == nil {
		homeDir = h
	}
	if homeDir != "" && strings.HasPrefix(path, homeDir) {
		return "~" + path[len(homeDir):]
	}
	return path
}

func getHomeDir() (string, error) {
	// Use os.UserHomeDir - this is a wrapper to avoid importing os here
	// since we already have access to it via provider implementations
	// For now, just try to get it from environment
	if home := getEnv("HOME"); home != "" {
		return home, nil
	}
	if userProfile := getEnv("USERPROFILE"); userProfile != "" {
		return userProfile, nil
	}
	return "", fmt.Errorf("home directory not found")
}

func getEnv(key string) string {
	// Simple wrapper - we can use os.Getenv directly but this keeps the code clean
	// and allows for future abstraction if needed
	return envLookup(key)
}

// envLookup is a variable so it can be mocked in tests
var envLookup = func(key string) string {
	return os.Getenv(key)
}

// runAuthImport implements the auth import command.
func runAuthImport(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	name, _ := cmd.Flags().GetString("name")
	description, _ := cmd.Flags().GetString("description")
	force, _ := cmd.Flags().GetBool("force")
	source, _ := cmd.Flags().GetString("source")
	jsonOutput, _ := cmd.Flags().GetBool("json")

	// Validate provider
	prov, ok := registry.Get(tool)
	if !ok {
		return fmt.Errorf("unknown agent: %s (supported: claude, codex, gemini)", tool)
	}

	// Check if profile exists
	if profileStore.Exists(tool, name) && !force {
		return fmt.Errorf("profile %s/%s already exists (use --force to overwrite)", tool, name)
	}

	// Determine source file
	var sourcePath string
	if source != "" {
		// Use explicit source path
		sourcePath = source
		// Expand ~ to home directory
		if strings.HasPrefix(sourcePath, "~/") {
			if home, err := getHomeDir(); err == nil {
				sourcePath = home + sourcePath[1:]
			}
		}
		// Verify source exists
		if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
			return fmt.Errorf("source file not found: %s", source)
		}
	} else {
		// Auto-detect auth
		detection, err := prov.DetectExistingAuth()
		if err != nil {
			return fmt.Errorf("detect auth: %w", err)
		}
		if !detection.Found || detection.Primary == nil {
			return fmt.Errorf("no existing auth detected for %s; run 'caam auth detect %s' to see details or use --source", tool, tool)
		}
		sourcePath = detection.Primary.Path
	}

	result := AuthImportResult{
		Provider:    tool,
		ProfileName: name,
		SourceFile:  sourcePath,
	}

	// Delete existing profile if force is set
	if profileStore.Exists(tool, name) && force {
		if err := profileStore.Delete(tool, name); err != nil {
			result.Error = fmt.Sprintf("delete existing profile: %v", err)
			if jsonOutput {
				return outputImportResult(result)
			}
			return fmt.Errorf("delete existing profile: %w", err)
		}
	}

	// Create profile
	// Use "oauth" as default auth mode since we're importing existing auth
	prof, err := profileStore.Create(tool, name, "oauth")
	if err != nil {
		result.Error = fmt.Sprintf("create profile: %v", err)
		if jsonOutput {
			return outputImportResult(result)
		}
		return fmt.Errorf("create profile: %w", err)
	}

	// Set description if provided
	if description != "" {
		prof.Description = description
	}

	// Save profile
	if err := prof.Save(); err != nil {
		profileStore.Delete(tool, name)
		result.Error = fmt.Sprintf("save profile: %v", err)
		if jsonOutput {
			return outputImportResult(result)
		}
		return fmt.Errorf("save profile: %w", err)
	}

	// Prepare profile directory structure
	ctx := context.Background()
	if err := prov.PrepareProfile(ctx, prof); err != nil {
		profileStore.Delete(tool, name)
		result.Error = fmt.Sprintf("prepare profile: %v", err)
		if jsonOutput {
			return outputImportResult(result)
		}
		return fmt.Errorf("prepare profile: %w", err)
	}

	// Import auth files
	copiedFiles, err := prov.ImportAuth(ctx, sourcePath, prof)
	if err != nil {
		profileStore.Delete(tool, name)
		result.Error = fmt.Sprintf("import auth: %v", err)
		if jsonOutput {
			return outputImportResult(result)
		}
		return fmt.Errorf("import auth: %w", err)
	}

	result.Success = true
	result.ProfilePath = prof.BasePath
	result.CopiedFiles = copiedFiles

	if jsonOutput {
		return outputImportResult(result)
	}

	// Print success message
	displayName := getProviderDisplayName(tool)
	fmt.Printf("Successfully imported %s auth to profile '%s'\n", displayName, name)
	fmt.Printf("\n")
	fmt.Printf("  Profile: %s/%s\n", tool, name)
	fmt.Printf("  Path: %s\n", prof.BasePath)
	fmt.Printf("  Source: %s\n", shortenPath(sourcePath))
	fmt.Printf("  Files copied:\n")
	for _, f := range copiedFiles {
		fmt.Printf("    - %s\n", shortenPath(f))
	}
	fmt.Printf("\n")
	fmt.Printf("Next steps:\n")
	fmt.Printf("  Run the agent with: caam exec %s %s -- <your command>\n", tool, name)
	fmt.Printf("  Or activate profile: eval \"$(caam env %s %s)\"\n", tool, name)

	return nil
}

func outputImportResult(result AuthImportResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	if !result.Success {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}
