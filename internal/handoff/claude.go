package handoff

// ClaudeLoginHandler is the handoff knowledge for Claude Code.
type ClaudeLoginHandler struct{}

// Provider returns the provider ID.
func (h *ClaudeLoginHandler) Provider() string {
	return "claude"
}

// ResumeArgs are the flags that reopen the most recent session, so a
// switched account is in use at once without losing the conversation.
func (h *ClaudeLoginHandler) ResumeArgs() []string {
	return []string{"--continue"}
}

// Ensure ClaudeLoginHandler implements LoginHandler.
var _ LoginHandler = (*ClaudeLoginHandler)(nil)
