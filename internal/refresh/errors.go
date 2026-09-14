package refresh

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported indicates that a refresh operation is not supported or not
// configured for a given provider/profile.
//
// Callers should generally treat this as a "skipped" outcome rather than a hard
// failure.
var ErrUnsupported = errors.New("refresh unsupported")

// ErrRefreshTokenReused indicates that the provider refused the refresh
// token: it has already been spent, or the session behind it is gone.
// OpenAI implements single-use refresh token rotation, so a token used once
// is refused ever after; Kimi answers 401/403 or invalid_grant when the
// session has ended.
//
// Common causes: a parallel refresh race (caam vs the provider's CLI), a
// stale vault copy restored after a newer refresh already consumed the
// token, or the CLI refreshing in the background while caam holds an older
// copy.
//
// The only recovery is to re-authenticate: 'caam login <provider> <profile>'.
var ErrRefreshTokenReused = errors.New("refresh token reused")

// UnsupportedError is returned when refresh cannot be performed for a provider
// due to missing required configuration or unsupported auth file formats.
type UnsupportedError struct {
	Provider string
	Reason   string
}

func (e *UnsupportedError) Error() string {
	if e == nil {
		return "refresh unsupported"
	}

	switch {
	case e.Provider == "" && e.Reason == "":
		return "refresh unsupported"
	case e.Provider == "":
		return fmt.Sprintf("refresh unsupported: %s", e.Reason)
	case e.Reason == "":
		return fmt.Sprintf("%s refresh unsupported", e.Provider)
	default:
		return fmt.Sprintf("%s refresh unsupported: %s", e.Provider, e.Reason)
	}
}

func (e *UnsupportedError) Unwrap() error {
	return ErrUnsupported
}

// RefreshTokenReusedError is returned when the provider rejects a refresh
// token because it has already been consumed (single-use token rotation)
// or the session behind it has ended.
type RefreshTokenReusedError struct {
	Provider string
	Profile  string
}

func (e *RefreshTokenReusedError) Error() string {
	loginCmd := fmt.Sprintf("caam login %s %s", e.Provider, e.Profile)
	return fmt.Sprintf(
		"%s/%s: the provider refused the refresh token (refresh_token_reused): "+
			"another process (the %s CLI or a parallel caam refresh) spent it, or the session has ended. "+
			"Re-authenticate with: %s",
		e.Provider, e.Profile, e.Provider, loginCmd)
}

func (e *RefreshTokenReusedError) Unwrap() error {
	return ErrRefreshTokenReused
}

// IsRefreshTokenReused checks if an error body from an OAuth token endpoint
// contains the refresh_token_reused error code. This is specific to OpenAI's
// single-use refresh token rotation.
func IsRefreshTokenReused(body string) bool {
	return strings.Contains(body, "refresh_token_reused")
}
