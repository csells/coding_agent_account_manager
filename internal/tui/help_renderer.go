package tui

import (
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// HelpRenderer renders markdown help content with Glamour and caches results.
type HelpRenderer struct {
	mu        sync.RWMutex
	theme     Theme
	width     int
	cache     map[string]string // key: contentHash, value: rendered
	renderer  *glamour.TermRenderer
	noGlamour bool // fallback for NO_COLOR or render failures
}

// NewHelpRenderer creates a new HelpRenderer with the given theme.
func NewHelpRenderer(theme Theme) *HelpRenderer {
	hr := &HelpRenderer{
		theme: theme,
		cache: make(map[string]string),
	}
	hr.initRenderer()
	return hr
}

// initRenderer creates the Glamour renderer based on theme.
func (hr *HelpRenderer) initRenderer() {
	if hr.theme.NoColor {
		hr.noGlamour = true
		return
	}

	// Choose style based on theme mode
	styleName := "dark"
	if hr.theme.Mode == ThemeLight {
		styleName = "light"
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(styleName),
		glamour.WithWordWrap(0), // We'll handle width ourselves
	)
	if err != nil {
		hr.noGlamour = true
		return
	}
	hr.renderer = renderer
}

// SetWidth updates the word wrap width for rendering.
func (hr *HelpRenderer) SetWidth(width int) {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	if hr.width != width {
		hr.width = width
		hr.cache = make(map[string]string) // clear cache on width change
	}
}

// Render renders markdown content, returning cached result if available.
func (hr *HelpRenderer) Render(markdown string) string {
	hr.mu.RLock()
	if cached, ok := hr.cache[markdown]; ok {
		hr.mu.RUnlock()
		return cached
	}
	hr.mu.RUnlock()

	var rendered string
	if hr.noGlamour || hr.renderer == nil {
		rendered = markdown
	} else {
		out, err := hr.renderer.Render(markdown)
		if err != nil {
			rendered = markdown
		} else {
			rendered = out
		}
	}

	hr.mu.Lock()
	hr.cache[markdown] = rendered
	hr.mu.Unlock()

	return rendered
}

// ContextualHint represents a context-sensitive help hint.
type ContextualHint struct {
	Key         string // e.g., "Enter"
	Description string // e.g., "Activate profile"
}

// GetContextualHints returns hints relevant to the current view state.
func GetContextualHints(state viewState) []ContextualHint {
	base := []ContextualHint{
		{"?", "Help"},
		{"q", "Quit"},
	}

	switch state {
	case stateList:
		return append([]ContextualHint{
			{"↑↓", "Navigate"},
			{"Enter", "Activate"},
			{"n", "New login"},
			{"/", "Search"},
			{"u", "Usage"},
		}, base...)

	case stateDetail:
		return append([]ContextualHint{
			{"Enter", "Confirm"},
			{"Esc", "Back"},
			{"e", "Edit"},
			{"d", "Delete"},
		}, base...)

	case stateSearch:
		return append([]ContextualHint{
			{"Enter", "Confirm"},
			{"Esc", "Cancel"},
			{"↑↓", "Navigate"},
		}, base...)

	case stateHelp:
		return []ContextualHint{
			{"Any key", "Return"},
		}

	case stateNameDialog:
		return append([]ContextualHint{
			{"Enter", "Save"},
			{"Esc", "Cancel"},
		}, base...)

	case stateConfirm, stateConfirmOverwrite, stateExportConfirm, stateImportConfirm:
		return []ContextualHint{
			{"y/Enter", "Confirm"},
			{"n/Esc", "Cancel"},
		}

	default:
		return base
	}
}

// RenderHintBar renders a compact hint bar for the status line.
func RenderHintBar(hints []ContextualHint, theme Theme, width int) string {
	if len(hints) == 0 {
		return ""
	}

	p := theme.Palette

	keyStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(p.KeycapText).
		Background(p.KeycapBg).
		Padding(0, 1)

	descStyle := lipgloss.NewStyle().
		Foreground(p.Muted)

	var parts []string
	currentWidth := 0
	maxWidth := width - 4 // leave some margin

	for _, h := range hints {
		part := keyStyle.Render(h.Key) + " " + descStyle.Render(h.Description)
		partWidth := lipgloss.Width(part) + 2 // separator space

		if currentWidth+partWidth > maxWidth {
			break
		}

		parts = append(parts, part)
		currentWidth += partWidth
	}

	return lipgloss.JoinHorizontal(lipgloss.Center, joinWithSeparator(parts, "  ")...)
}

func joinWithSeparator(parts []string, sep string) []string {
	if len(parts) == 0 {
		return nil
	}
	result := make([]string, 0, len(parts)*2-1)
	for i, p := range parts {
		if i > 0 {
			result = append(result, sep)
		}
		result = append(result, p)
	}
	return result
}

// MainHelpMarkdown returns the full help content as Markdown.
func MainHelpMarkdown() string {
	return `# caam - Coding Agent Account Manager

## Keyboard Shortcuts

### Navigation
| Key | Action |
|-----|--------|
| ↑/k | Move up |
| ↓/j | Move down |
| ←/h | Previous agent |
| →/l | Next agent |
| Tab | Cycle agents |
| / | Search accounts |

### Account Actions
| Key | Action |
|-----|--------|
| Enter | Switch to the selected account (instant, with confirmation) |
| n | Log in to a new account (captures the current one, clears it, runs the agent's login, captures the new one) |
| r | Refresh limits, and an expired token first |
| i | Full account card |
| e | Edit account settings |
| o | Open account page in browser |
| d | Delete account (with confirmation) |
| p | Set project association for current directory |

### Vault & Data
| Key | Action |
|-----|--------|
| u | Toggle usage stats panel (1/2/3/4 for time ranges) |
| S | Toggle sync panel |
| E | Export vault to a zip bundle in the current directory (not encrypted) |
| I | Import vault from bundle |

### General
| Key | Action |
|-----|--------|
| ctrl+p | Command palette |
| ? | Toggle this help |
| q/Esc | Quit |

---

## Health Status Indicators

| Icon | Status | Meaning |
|------|--------|---------|
| 🟢 | Healthy | Token valid >1hr, no recent errors |
| 🟡 | Warning | Token expiring soon or minor issues |
| 🔴 | Critical | Token expired or repeated errors |
| ⚪ | Unknown | Health data not available |

---

## Smart Profile Features (CLI)

` + "```" + `bash
caam activate <agent> --auto    # Smart rotation picks best profile
caam run <agent> -- <args>      # Wrap the agent with auto-failover on rate limits
caam cooldown set <profile>     # Mark profile as rate-limited
caam cooldown list              # View active cooldowns
caam next <agent>               # Preview which profile rotation would pick
` + "```" + `

### Rotation Algorithms
Configure in ` + "`config.yaml`" + ` → ` + "`stealth.rotation.algorithm`" + `:

- **smart** — Multi-factor scoring: health, cooldown, recency, plan type
- **round_robin** — Sequential cycling through profiles
- **random** — Random selection

---

## Project Associations

Profiles can be linked to directories. When you activate in a project directory,
caam uses the associated profile automatically.

---

## Agent Notes

To add an account to any agent, press n: the dashboard captures the
account that is signed in now, clears it so the agent's login cannot
revoke it, runs the login, and captures the new account under its identity.

### Claude Code
- Identity is read from the credential; the new account is filed under its email.
- Claude renews its own tokens; when a session has ended, r offers the login.

### Codex CLI
- Identity is read from the JWT; the new account is filed under its email.
- r refreshes an expired token before fetching limits.

### Gemini CLI
- Identity is read from the credential; the new account is filed under its email.
- r refreshes an expired token before fetching limits.

---

*Press any key to return*
`
}
