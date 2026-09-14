package testutil

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// SyntheticCodexAuth builds a ChatGPT-mode Codex auth.json whose id_token
// names email and whose tokens carry iat, the way Codex writes it after a
// login or an in-place refresh. The JWTs are unsigned and synthetic; the
// refresh token is "SYNTHETIC-REFRESH-<tag>". Two calls with the same
// email and different tags read as the same account with rotated tokens.
func SyntheticCodexAuth(t testing.TB, email, tag string, issuedAt int64) []byte {
	t.Helper()
	jwt := func(claims map[string]any) string {
		payload, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
			base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	}
	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": email, "iat": issuedAt, "exp": issuedAt + 3600}),
			"access_token":  jwt(map[string]any{"sub": email, "iat": issuedAt, "exp": issuedAt + 3600}),
			"refresh_token": "SYNTHETIC-REFRESH-" + tag,
		},
		"last_refresh": time.Unix(issuedAt, 0).UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
