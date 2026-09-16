package usage

import "strings"

// ShortError condenses a usage-fetch error (a UsageInfo.Error, or the error
// a fetcher returned) to a phrase that fits a table cell or a card row. The
// common refusals become a fixed hint — an expired token, a login with no
// limits API (ErrNoOpenCodeLimitsAPI), a zcode login without a plan
// (ErrNoZcodePlan), a missing credential, a refused quota call, a provider
// without usage support; anything else is cut at its first ";" (the remedy
// clause) and then to width characters.
func ShortError(msg string, width int) string {
	e := strings.ToLower(msg)
	switch {
	case AuthRefused(msg):
		return "auth expired (re-login)"
	case strings.Contains(e, "no usage api"), strings.Contains(e, "no limits api"):
		return "no limits API"
	case strings.Contains(e, "no z.ai coding plan"):
		return "no coding plan"
	case strings.Contains(e, "no credential"):
		return "no credential captured"
	case strings.Contains(e, "permission_denied"), strings.Contains(e, "403"):
		return "quota API refused (403)"
	case strings.Contains(e, "missing access token"), strings.Contains(e, "not logged in"),
		strings.Contains(e, "no access token"):
		return "not logged in"
	case strings.Contains(e, "not yet supported"), strings.Contains(e, "unsupported"),
		strings.Contains(e, "not supported"):
		return "usage not supported"
	}
	if i := strings.Index(msg, ";"); i > 0 {
		msg = msg[:i]
	}
	if len(msg) > width {
		return msg[:width-3] + "..."
	}
	return msg
}

// AuthRefused reports whether a usage-fetch error means the service refused
// the account's token. A revoked refresh-token family leaves an access token
// whose expiry is days away, so the vault copy looks healthy while every call
// with it is refused; this is the fact that must win over the expiry date.
func AuthRefused(msg string) bool {
	e := strings.ToLower(msg)
	return strings.Contains(e, "unauthorized") || strings.Contains(e, "token expired") ||
		strings.Contains(e, "expired or invalid") || strings.Contains(e, "401")
}
