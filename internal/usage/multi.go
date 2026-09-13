package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
)

// ProfileUsage combines usage info with profile metadata.
type ProfileUsage struct {
	Provider    string     `json:"provider"`
	ProfileName string     `json:"profile_name"`
	Usage       *UsageInfo `json:"usage"`
	AccessToken string     `json:"-"` // Not serialized

	// CredentialSource names which of caam's credential namespaces this row
	// was actually read from, and what else holds the same profile name.
	// One name can exist in the vault, in an isolated profile and in a
	// shallow profile at once, holding different (and differently fresh)
	// credentials; without this a caller cannot tell which copy produced the
	// verdict (issue #100). Populated for single-profile lookups.
	CredentialSource *CredentialSource `json:"credential_source,omitempty"`
}

// CredentialSource describes the credential namespace a row was read from.
type CredentialSource struct {
	// Namespace is "vault", "isolated" or "shallow".
	Namespace string `json:"namespace"`
	// Path is the credential file (or, for an offline read, the .claude.json)
	// that was actually opened.
	Path string `json:"path"`
	// State is caam's offline read of that credential: "healthy", "expired",
	// "unknown", or "missing".
	State string `json:"state"`
	// Explicit reports whether the caller named the namespace (--source)
	// rather than taking the default.
	Explicit bool `json:"explicit"`
	// Alternatives lists the other namespaces holding the same profile name.
	// Credentials are never copied between them implicitly.
	Alternatives []CredentialAlternative `json:"alternatives,omitempty"`
}

// CredentialAlternative is one namespace that also holds the profile name.
type CredentialAlternative struct {
	Namespace string `json:"namespace"`
	Path      string `json:"path"`
	State     string `json:"state"`
	// Healthier reports that this copy is in a strictly better state than the
	// one that was read.
	Healthier bool `json:"healthier"`
}

// MultiProfileFetcher fetches usage data for multiple profiles concurrently.
type MultiProfileFetcher struct {
	claudeFetcher *ClaudeFetcher
	codexFetcher  *CodexFetcher
	agyFetcher    *AgyFetcher
	kimiFetcher   *KimiFetcher
	zcodeFetcher  *ZcodeFetcher
	opencode      *OpenCodeFetcher
	logScanner    logs.Scanner // Optional scanner for burn rate calculation
}

// FetcherOption configures the MultiProfileFetcher.
type FetcherOption func(*MultiProfileFetcher)

// WithLogScanner sets the log scanner for burn rate calculation.
func WithLogScanner(scanner logs.Scanner) FetcherOption {
	return func(m *MultiProfileFetcher) {
		m.logScanner = scanner
	}
}

// NewMultiProfileFetcher creates a new multi-profile fetcher.
func NewMultiProfileFetcher(opts ...FetcherOption) *MultiProfileFetcher {
	m := &MultiProfileFetcher{
		claudeFetcher: NewClaudeFetcher(),
		codexFetcher:  NewCodexFetcher(),
		agyFetcher:    NewAgyFetcher(),
		kimiFetcher:   NewKimiFetcher(),
		zcodeFetcher:  NewZcodeFetcher(),
		opencode:      NewOpenCodeFetcher(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// FetchAllProfiles fetches usage for all profiles of a given provider.
// profiles is a map of profile name to access token.
func (m *MultiProfileFetcher) FetchAllProfiles(ctx context.Context, provider string, profiles map[string]string) []ProfileUsage {
	if m == nil {
		m = &MultiProfileFetcher{}
	}

	var wg sync.WaitGroup
	results := make([]ProfileUsage, 0, len(profiles))
	var mu sync.Mutex

	for name, token := range profiles {
		wg.Add(1)
		go func(name, token string) {
			defer wg.Done()

			var info *UsageInfo
			var err error

			switch {
			case token == "" && provider == "opencode":
				// A login with nothing a usage API accepts. Say so rather
				// than presenting the account as idle or dropping it.
				info = &UsageInfo{
					Provider:  provider,
					FetchedAt: time.Now(),
					Error:     ErrNoOpenCodeLimitsAPI,
				}
			default:
				info, err = m.fetchOne(ctx, provider, token)
			}

			if info == nil {
				errMsg := "usage fetcher returned no data"
				if err != nil {
					errMsg = err.Error()
				}
				info = &UsageInfo{
					Provider:  provider,
					FetchedAt: time.Now(),
					Error:     errMsg,
				}
			} else if err != nil && info.Error == "" {
				info.Error = err.Error()
			}

			if info != nil {
				info.ProfileName = name

				// Calculate burn rate if scanner is available
				if m.logScanner != nil {
					// Scan logs for this provider.
					// Note: Currently logs.Scanner interface takes logDir.
					// We might need to know the specific log directory for the profile if isolated,
					// or use the provider's default log dir and filter by user?
					// For now, we'll scan the provider's default logs and we might need to filter by profile?
					//
					// CAAM usually runs one profile at a time in Vault mode, so the logs in ~/.local/share/claude/logs
					// belong to the *active* profile at that time. But historical logs might be mixed.
					//
					// However, typical CLI usage is sequential.
					// We'll scan the last 24 hours of logs.

					// Use a 24-hour window for burn rate calculation
					window := 24 * time.Hour
					since := time.Now().Add(-window)

					// We need to find the correct log directory.
					// For Vault mode, it's the standard provider log dir.
					// But MultiProfileFetcher doesn't know about file system paths easily.
					// We'll rely on the scanner's default behavior if logDir is empty.
					//
					// If using MultiScanner, we need to cast or select the right scanner.
					var scanner logs.Scanner
					if ms, ok := m.logScanner.(*logs.MultiScanner); ok {
						scanner = ms.Scanner(provider)
					} else {
						scanner = m.logScanner
					}

					if scanner != nil {
						scanRes, err := scanner.Scan(ctx, "", since)
						if err == nil && scanRes != nil {
							// Filter logs?
							// If we have profile-specific logs, great.
							// For now, assume all logs for the provider are relevant usage
							// (as we are usually checking *our* usage).
							//
							// TODO: If we want to be precise per-profile, we'd need logs to contain
							// some identity info, which they often don't.
							// But for "burn rate", recent usage is what matters.

							burnRate := CalculateBurnRate(scanRes.Entries, window, DefaultBurnRateOptions())
							if burnRate != nil {
								info.BurnRate = burnRate
								info.UpdateDepletion()
							}
						}
					}
				}
			}

			mu.Lock()
			results = append(results, ProfileUsage{
				Provider:    provider,
				ProfileName: name,
				Usage:       info,
				AccessToken: token,
			})
			mu.Unlock()
		}(name, token)
	}

	wg.Wait()

	// Sort by availability score (highest first)
	sort.Slice(results, func(i, j int) bool {
		scoreI := 0
		scoreJ := 0
		if results[i].Usage != nil {
			scoreI = results[i].Usage.AvailabilityScore()
		}
		if results[j].Usage != nil {
			scoreJ = results[j].Usage.AvailabilityScore()
		}
		if scoreI == scoreJ {
			return results[i].ProfileName < results[j].ProfileName
		}
		return scoreI > scoreJ
	})

	return results
}

// fetchOne runs the provider's fetcher for one token.
func (m *MultiProfileFetcher) fetchOne(ctx context.Context, provider, token string) (*UsageInfo, error) {
	var info *UsageInfo
	var err error
	switch provider {
	case "claude":
		if m.claudeFetcher == nil {
			info = &UsageInfo{
				Provider:  provider,
				FetchedAt: time.Now(),
				Error:     "claude fetcher unavailable",
			}
		} else {
			info, err = m.claudeFetcher.Fetch(ctx, token)
		}
	case "codex":
		if m.codexFetcher == nil {
			info = &UsageInfo{
				Provider:  provider,
				FetchedAt: time.Now(),
				Error:     "codex fetcher unavailable",
			}
		} else {
			info, err = m.codexFetcher.Fetch(ctx, token)
		}
	case "agy":
		if m.agyFetcher == nil {
			info = &UsageInfo{
				Provider:  provider,
				FetchedAt: time.Now(),
				Error:     "agy fetcher unavailable",
			}
		} else {
			info, err = m.agyFetcher.Fetch(ctx, token)
		}
	case "kimi":
		if m.kimiFetcher == nil {
			info = &UsageInfo{
				Provider:  provider,
				FetchedAt: time.Now(),
				Error:     "kimi fetcher unavailable",
			}
		} else {
			info, err = m.kimiFetcher.Fetch(ctx, token)
		}
	case "zcode":
		if m.zcodeFetcher == nil {
			info = &UsageInfo{
				Provider:  provider,
				FetchedAt: time.Now(),
				Error:     "zcode fetcher unavailable",
			}
		} else {
			info, err = m.zcodeFetcher.Fetch(ctx, token)
		}
	case "opencode":
		if m.opencode == nil {
			info = &UsageInfo{
				Provider:  provider,
				FetchedAt: time.Now(),
				Error:     "opencode fetcher unavailable",
			}
		} else {
			info, err = m.opencode.Fetch(ctx, token)
		}
	default:
		info = &UsageInfo{
			Provider:  provider,
			FetchedAt: time.Now(),
			Error:     fmt.Sprintf("unsupported provider: %s", provider),
		}
	}
	return info, err
}

// GetBestProfile returns the profile with the highest availability score.
func (m *MultiProfileFetcher) GetBestProfile(ctx context.Context, provider string, profiles map[string]string) *ProfileUsage {
	results := m.FetchAllProfiles(ctx, provider, profiles)
	if len(results) == 0 {
		return nil
	}
	return &results[0]
}

// GetProfilesAboveThreshold returns profiles with usage below the threshold.
// threshold is the maximum utilization (e.g., 0.8 for 80%).
func (m *MultiProfileFetcher) GetProfilesAboveThreshold(ctx context.Context, provider string, profiles map[string]string, threshold float64) []ProfileUsage {
	results := m.FetchAllProfiles(ctx, provider, profiles)
	available := make([]ProfileUsage, 0)

	for _, p := range results {
		if p.Usage != nil && !p.Usage.IsNearLimit(threshold) {
			available = append(available, p)
		}
	}

	return available
}

// ReadClaudeCredentials reads the access token from Claude credentials file.
func ReadClaudeCredentials(path string) (accessToken string, accountID string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	var creds struct {
		ClaudeAiOauth *struct {
			AccessToken string `json:"accessToken"`
			AccountID   string `json:"accountId"`
		} `json:"claudeAiOauth"`
		OAuthToken  json.RawMessage `json:"oauthToken"`
		AccessToken string          `json:"access_token"`
		AccessCamel string          `json:"accessToken"`
	}

	if err := json.Unmarshal(data, &creds); err != nil {
		return "", "", err
	}

	if creds.ClaudeAiOauth != nil {
		return creds.ClaudeAiOauth.AccessToken, creds.ClaudeAiOauth.AccountID, nil
	}

	if len(creds.OAuthToken) > 0 {
		var token string
		if err := json.Unmarshal(creds.OAuthToken, &token); err == nil && token != "" {
			return token, "", nil
		}

		var oauth struct {
			AccessToken string `json:"access_token"`
			AccessCamel string `json:"accessToken"`
		}
		if err := json.Unmarshal(creds.OAuthToken, &oauth); err == nil {
			if oauth.AccessToken != "" {
				return oauth.AccessToken, "", nil
			}
			if oauth.AccessCamel != "" {
				return oauth.AccessCamel, "", nil
			}
		}
	}
	if creds.AccessToken != "" {
		return creds.AccessToken, "", nil
	}
	if creds.AccessCamel != "" {
		return creds.AccessCamel, "", nil
	}

	return "", "", fmt.Errorf("no access token found in credentials")
}

// ReadCodexCredentials reads the access token from Codex auth file.
func ReadCodexCredentials(path string) (accessToken string, accountID string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	var creds struct {
		// Direct API key format
		OpenAIAPIKey string `json:"OPENAI_API_KEY"`

		// OAuth token format
		Tokens *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`

		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	}

	if err := json.Unmarshal(data, &creds); err != nil {
		return "", "", err
	}

	// Fall back to OAuth tokens
	if creds.Tokens != nil && creds.Tokens.AccessToken != "" {
		return creds.Tokens.AccessToken, creds.Tokens.AccountID, nil
	}

	if creds.AccessToken != "" {
		return creds.AccessToken, creds.AccountID, nil
	}

	// API key auth does not support ChatGPT usage endpoints.
	if creds.OpenAIAPIKey != "" {
		return "", "", fmt.Errorf("API key auth does not support usage fetch")
	}

	return "", "", fmt.Errorf("no access token found in credentials")
}

// CredentialFiles lists, in preference order, the file names that can hold a
// provider's access token — in a vault profile and in the provider's own live
// auth directory alike. An unknown provider has none.
func CredentialFiles(provider string) []string {
	switch provider {
	case "claude":
		// The current location first, then the two legacy ones.
		return []string{".credentials.json", ".claude.json", "auth.json"}
	case "codex":
		return []string{"auth.json"}
	case "agy":
		return []string{"antigravity-oauth-token"}
	case "kimi":
		return []string{"kimi-code.json"}
	case "zcode":
		return []string{"credentials.json"}
	case "opencode":
		// The export of opencode.db's auth tables, then the older auth.json.
		return []string{"opencode-auth.json", "auth.json"}
	}
	return nil
}

// ReadCredentials reads the access token (and the account id, when the file
// carries one) from one credential file of provider.
func ReadCredentials(provider, path string) (accessToken string, accountID string, err error) {
	switch provider {
	case "claude":
		return ReadClaudeCredentials(path)
	case "codex":
		return ReadCodexCredentials(path)
	case "agy":
		return ReadAgyCredentials(path)
	case "kimi":
		return ReadKimiCredentials(path)
	case "zcode":
		return ReadZcodeCredentials(path)
	case "opencode":
		return ReadOpenCodeCredentials(path)
	}
	return "", "", fmt.Errorf("no credential reader for provider %q", provider)
}

// LoadProfileCredentials loads credentials for all profiles of a provider from the vault.
func LoadProfileCredentials(vaultDir, provider string) (map[string]string, error) {
	providerDir := filepath.Join(vaultDir, provider)
	entries, err := os.ReadDir(providerDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No profiles
		}
		return nil, err
	}

	credentials := make(map[string]string)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		profileName := entry.Name()
		profileDir := filepath.Join(providerDir, profileName)

		// The first credential file that yields a token wins; a profile
		// whose files all fail to read is skipped — except one whose store
		// is present but holds no credential a usage API accepts, which
		// gets an empty token so the table can say so (see NoLimitsAPI).
		for _, name := range CredentialFiles(provider) {
			token, _, readErr := ReadCredentials(provider, filepath.Join(profileDir, name))
			if readErr == nil && token != "" {
				credentials[profileName] = token
				break
			}
			if readErr != nil && readErr.Error() == ErrNoOpenCodeLimitsAPI {
				credentials[profileName] = ""
				break
			}
		}
	}

	return credentials, nil
}
