package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// ProviderPanelStyles holds the styles the provider strip draws with.
type ProviderPanelStyles struct {
	Border          lipgloss.Style
	Title           lipgloss.Style
	Item            lipgloss.Style
	SelectedItem    lipgloss.Style
	Count           lipgloss.Style
	ActiveIndicator lipgloss.Style
}

// DefaultProviderPanelStyles returns the default styles for the provider panel.
func DefaultProviderPanelStyles() ProviderPanelStyles {
	return NewProviderPanelStyles(DefaultTheme())
}

// NewProviderPanelStyles returns themed styles for the provider panel.
func NewProviderPanelStyles(theme Theme) ProviderPanelStyles {
	p := theme.Palette

	return ProviderPanelStyles{
		Border: lipgloss.NewStyle().
			Border(theme.Border).
			BorderForeground(p.BorderMuted).
			Background(p.Surface).
			Padding(0, 1),

		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Accent).
			MarginBottom(1),

		Item: lipgloss.NewStyle().
			Foreground(p.Muted).
			PaddingLeft(1),

		SelectedItem: lipgloss.NewStyle().
			Foreground(p.Text).
			Bold(true).
			Background(p.Selection).
			PaddingLeft(1),

		Count: lipgloss.NewStyle().
			Foreground(p.Muted).
			Italic(true),

		ActiveIndicator: lipgloss.NewStyle().
			Foreground(p.Success).
			Bold(true),
	}
}

// providerLabel is the name a provider goes by on screen: the product's
// own name, not its caam id ("Antigravity", not "Agy"). It is the same
// vocabulary every other caam surface prints (provider.Label).
func providerLabel(id string) string {
	return provider.Label(id)
}
