package handoff

// CodexLoginHandler is the handoff knowledge for the Codex CLI.
type CodexLoginHandler struct{}

// Provider returns the provider ID.
func (h *CodexLoginHandler) Provider() string {
	return "codex"
}

// ResumeArgs are the flags that reopen the most recent session, so a
// switched account is in use at once without losing the conversation.
func (h *CodexLoginHandler) ResumeArgs() []string {
	return ResumeArgs(h.Provider())
}

// Ensure CodexLoginHandler implements LoginHandler.
var _ LoginHandler = (*CodexLoginHandler)(nil)
