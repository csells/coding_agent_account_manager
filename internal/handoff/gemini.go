package handoff

// GeminiLoginHandler is the handoff knowledge for the Gemini CLI.
type GeminiLoginHandler struct{}

// Provider returns the provider ID.
func (h *GeminiLoginHandler) Provider() string {
	return "gemini"
}

// ResumeArgs are the flags that reopen the most recent session, so a
// switched account is in use at once without losing the conversation.
func (h *GeminiLoginHandler) ResumeArgs() []string {
	return []string{"--resume", "latest"}
}

// Ensure GeminiLoginHandler implements LoginHandler.
var _ LoginHandler = (*GeminiLoginHandler)(nil)
