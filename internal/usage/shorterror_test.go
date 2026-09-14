package usage

import "testing"

// ShortError is the one phrase list behind the dashboard's card rows and
// the monitor's table cells; each surface passes its own width.
func TestShortError_PhrasesThenCutAtRemedyThenWidth(t *testing.T) {
	cases := []struct {
		msg   string
		width int
		want  string
	}{
		{"unauthorized: token expired or invalid; run `zcode login`, then retry", 48, "auth expired (re-login)"},
		{"401 Unauthorized", 40, "auth expired (re-login)"},
		{"token expired or invalid", 40, "auth expired (re-login)"},
		{ErrNoOpenCodeLimitsAPI, 48, "no limits API"},
		{"claude has no usage API", 48, "no limits API"},
		{ErrNoZcodePlan, 48, "no coding plan"},
		{"no credential captured for agy/x", 48, "no credential captured"},
		{"API error: status 403 (PERMISSION_DENIED: no valid license)", 48, "quota API refused (403)"},
		{"missing access token", 40, "not logged in"},
		{"no access token found in credentials", 40, "not logged in"},
		{"usage not yet supported for grok", 40, "usage not supported"},
		{"unsupported provider: foo", 40, "usage not supported"},
		{"API error: status 500; try again later", 48, "API error: status 500"},
		{"decode error: invalid character 'x' looking for beginning of value", 40, "decode error: invalid character 'x' l..."},
		{"decode error: invalid character 'x' looking for beginning of value", 48, "decode error: invalid character 'x' looking f..."},
		{"short", 40, "short"},
	}
	for _, c := range cases {
		if got := ShortError(c.msg, c.width); got != c.want {
			t.Errorf("ShortError(%q, %d) = %q, want %q", c.msg, c.width, got, c.want)
		}
	}
}
