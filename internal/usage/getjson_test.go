package usage

import (
	"strings"
	"testing"
)

// errorDetail names the API's own explanation of a refusal, in the shape
// each API sends it: Kimi as a top-level message/msg/error, Google nested
// under error as a status and a message.
func TestErrorDetail_FirstNamedFieldWins(t *testing.T) {
	cases := []struct {
		body  string
		paths []string
		want  string
	}{
		{`{"error":"invalid_token","message":"token expired"}`, []string{"message", "msg", "error"}, " (token expired)"},
		{`{"error":"invalid_token","msg":" busy "}`, []string{"message", "msg", "error"}, " (busy)"},
		{`{"error":"invalid_token"}`, []string{"message", "msg", "error"}, " (invalid_token)"},
		{`{"error":{"message":"Invalid Credentials","status":"UNAUTHENTICATED"}}`, []string{"error.message"}, " (Invalid Credentials)"},
		{`{"error":{"status":"UNAUTHENTICATED"}}`, []string{"error.message", "error.status"}, " (UNAUTHENTICATED)"},
		{`{"message":""}`, []string{"message"}, ""},
		{`not json`, []string{"message"}, ""},
		{`{"message":42}`, []string{"message"}, ""},
	}
	for _, c := range cases {
		if got := errorDetail([]byte(c.body), c.paths...); got != c.want {
			t.Errorf("errorDetail(%s, %v) = %q, want %q", c.body, c.paths, got, c.want)
		}
	}
	long := `{"message":"` + strings.Repeat("x", 200) + `"}`
	if got := errorDetail([]byte(long), "message"); len(got) != 163 || !strings.HasSuffix(got, "...)") {
		t.Errorf("a long message is cut to 160: got %d chars %q", len(got), got)
	}
}

func TestGoogleErrorDetail_StatusAndMessage(t *testing.T) {
	body := `{"error":{"code":403,"message":"no valid license (#3501)","status":"PERMISSION_DENIED"}}`
	if got, want := googleErrorDetail([]byte(body)), " (PERMISSION_DENIED: no valid license (#3501))"; got != want {
		t.Errorf("googleErrorDetail = %q, want %q", got, want)
	}
	if got := googleErrorDetail([]byte(`{"error":{"code":500}}`)); got != "" {
		t.Errorf("a body without status or message = %q, want empty", got)
	}
}
