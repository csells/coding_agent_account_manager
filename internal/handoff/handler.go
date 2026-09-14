// Package handoff names, per provider, how a switched account is put to use
// in a running session: the flags that reopen the tool's most recent
// conversation. The smart runner ends the session after a switch and
// respawns the tool with them; nothing is typed into a login.
package handoff

import (
	"sync"
)

// LoginHandler is a provider's handoff knowledge.
type LoginHandler interface {
	// Provider returns the provider ID this handler supports (e.g., "claude").
	Provider() string

	// ResumeArgs are the flags that reopen the tool's most recent session,
	// so a switched account is in use at once with the conversation kept.
	ResumeArgs() []string
}

// Registry manages provider-to-handler mappings.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]LoginHandler
}

// NewRegistry creates a new handler registry with default handlers.
func NewRegistry() *Registry {
	r := &Registry{
		handlers: make(map[string]LoginHandler),
	}

	// Register default handlers
	r.Register(&ClaudeLoginHandler{})
	r.Register(&CodexLoginHandler{})
	r.Register(&GeminiLoginHandler{})

	return r
}

// Register adds a handler to the registry.
func (r *Registry) Register(h LoginHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[h.Provider()] = h
}

// Get returns the handler for a provider, or nil if not found.
func (r *Registry) Get(provider string) LoginHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handlers[provider]
}

// Clear removes a handler from the registry. Useful for testing.
func (r *Registry) Clear(provider string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.handlers, provider)
}

// Providers returns a list of registered provider IDs.
func (r *Registry) Providers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providers := make([]string, 0, len(r.handlers))
	for p := range r.handlers {
		providers = append(providers, p)
	}
	return providers
}

// DefaultRegistry is the global handler registry.
var DefaultRegistry = NewRegistry()

// GetHandler returns the handler for a provider from the default registry.
func GetHandler(provider string) LoginHandler {
	return DefaultRegistry.Get(provider)
}
