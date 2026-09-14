package refresh

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// kimiTokenServer answers the token endpoint with status and body and
// records the last request it saw (headers and parsed form).
type kimiTokenServer struct {
	*httptest.Server
	status  int
	body    string
	method  string
	path    string
	header  http.Header
	form    map[string]string
	ctype   string
	request int
}

func newKimiTokenServer(t *testing.T, status int, body string) *kimiTokenServer {
	t.Helper()
	s := &kimiTokenServer{status: status, body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.request++
		s.method = r.Method
		s.path = r.URL.Path
		s.header = r.Header.Clone()
		s.ctype = r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		s.form = map[string]string{}
		for k := range r.PostForm {
			s.form[k] = r.PostForm.Get(k)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.Close)
	return s
}

// pointKimiAt sends RefreshKimiToken to the test server for the test's
// duration and clears the CLI's host overrides so they cannot interfere.
func pointKimiAt(t *testing.T, s *kimiTokenServer) {
	t.Helper()
	t.Setenv("KIMI_CODE_OAUTH_HOST", "")
	t.Setenv("KIMI_OAUTH_HOST", "")
	old := KimiTokenURL
	KimiTokenURL = s.URL + KimiTokenPath
	t.Cleanup(func() { KimiTokenURL = old })
}

// The refresh is the Kimi Code CLI's own: a form-encoded POST to
// /api/oauth/token carrying its client id, sent with the same device
// identity headers as every other request the CLI makes.
func TestRefreshKimiToken_PostsTheCLIsForm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "device_id"), []byte("dev-1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newKimiTokenServer(t, http.StatusOK, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"token_type":"Bearer","scope":"kimi-code"}`)
	pointKimiAt(t, s)

	resp, err := RefreshKimiToken(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("RefreshKimiToken: %v", err)
	}
	if resp.AccessToken != "new-access" || resp.RefreshToken != "new-refresh" || resp.ExpiresIn != 3600 {
		t.Errorf("response = %+v", resp)
	}

	if s.method != http.MethodPost || s.path != "/api/oauth/token" {
		t.Errorf("request = %s %s, want POST /api/oauth/token", s.method, s.path)
	}
	if !strings.HasPrefix(s.ctype, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q, want a form", s.ctype)
	}
	want := map[string]string{
		"client_id":     KimiClientID,
		"grant_type":    "refresh_token",
		"refresh_token": "old-refresh",
	}
	for k, v := range want {
		if s.form[k] != v {
			t.Errorf("form[%s] = %q, want %q", k, s.form[k], v)
		}
	}
	if len(s.form) != len(want) {
		t.Errorf("form carries extra fields: %v", s.form)
	}
	if s.header.Get("X-Msh-Platform") != "kimi_code_cli" || s.header.Get("X-Msh-Device-Id") != "dev-1234" || s.header.Get("X-Msh-Version") == "" {
		t.Errorf("device headers missing: %v", s.header)
	}
	if s.header.Get("Authorization") != "" {
		t.Error("a refresh presents the refresh token in the form, never a bearer header")
	}
}

// 401, 403 or invalid_grant from the token endpoint mean the session is
// gone: the error is the same class a spent Codex refresh token gets, so
// every caller already says "log in again". Anything else is a plain
// failure that a later retry may clear.
func TestRefreshKimiToken_SessionGoneIsTerminal(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		gone   bool
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":"unauthorized"}`, true},
		{"forbidden", http.StatusForbidden, ``, true},
		{"invalid_grant", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"refresh token revoked"}`, true},
		{"server error", http.StatusBadGateway, `bad gateway`, false},
		{"other 400", http.StatusBadRequest, `{"error":"invalid_request"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KIMI_CODE_HOME", t.TempDir())
			s := newKimiTokenServer(t, tc.status, tc.body)
			pointKimiAt(t, s)
			_, err := RefreshKimiToken(context.Background(), "old-refresh")
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, ErrRefreshTokenReused) != tc.gone {
				t.Errorf("errors.Is(ErrRefreshTokenReused) = %v, want %v (err %v)", !tc.gone, tc.gone, err)
			}
			if !tc.gone && !strings.Contains(err.Error(), strings.TrimSpace(tc.body)) && tc.body != "" {
				t.Errorf("a plain failure keeps the body for the log: %v", err)
			}
		})
	}
}

// The CLI lets KIMI_CODE_OAUTH_HOST, then KIMI_OAUTH_HOST, replace its
// OAuth host; caam refreshes against the same host the CLI would.
func TestRefreshKimiToken_HonoursTheCLIsOAuthHost(t *testing.T) {
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	code := newKimiTokenServer(t, http.StatusOK, `{"access_token":"a","expires_in":60}`)
	plain := newKimiTokenServer(t, http.StatusOK, `{"access_token":"a","expires_in":60}`)

	old := KimiTokenURL
	KimiTokenURL = defaultKimiTokenURL
	t.Cleanup(func() { KimiTokenURL = old })

	t.Setenv("KIMI_CODE_OAUTH_HOST", "")
	t.Setenv("KIMI_OAUTH_HOST", plain.URL+"/")
	if _, err := RefreshKimiToken(context.Background(), "rt"); err != nil {
		t.Fatalf("KIMI_OAUTH_HOST: %v", err)
	}
	if plain.request != 1 || plain.path != "/api/oauth/token" {
		t.Errorf("KIMI_OAUTH_HOST not honoured: %d requests to %q", plain.request, plain.path)
	}

	t.Setenv("KIMI_CODE_OAUTH_HOST", code.URL)
	if _, err := RefreshKimiToken(context.Background(), "rt"); err != nil {
		t.Fatalf("KIMI_CODE_OAUTH_HOST: %v", err)
	}
	if code.request != 1 || plain.request != 1 {
		t.Errorf("KIMI_CODE_OAUTH_HOST should win: code=%d plain=%d", code.request, plain.request)
	}
}

// The endpoint is pinned to Kimi's auth hosts (and loopback for tests):
// a stray override cannot send the refresh token anywhere else.
func TestRefreshKimiToken_RefusesAForeignHost(t *testing.T) {
	t.Setenv("KIMI_CODE_OAUTH_HOST", "")
	t.Setenv("KIMI_OAUTH_HOST", "")
	old := KimiTokenURL
	KimiTokenURL = "https://auth.example.com/api/oauth/token"
	t.Cleanup(func() { KimiTokenURL = old })
	if _, err := RefreshKimiToken(context.Background(), "rt"); err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("expected the host to be refused, got %v", err)
	}
	if _, err := RefreshKimiToken(context.Background(), ""); err == nil {
		t.Fatal("an empty refresh token is refused before any request")
	}
}

// UpdateKimiAuth rewrites the tokens and the expiry in kimi-code.json and
// leaves everything else as the CLI wrote it.
func TestUpdateKimiAuth_RewritesOnlyTheTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kimi-code.json")
	writeJSON(t, path, map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expires_at":    1000,
		"expires_in":    900,
		"scope":         "kimi-code",
		"token_type":    "Bearer",
		"extra":         "kept",
	})

	before := time.Now().Unix()
	if err := UpdateKimiAuth(path, &TokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600}); err != nil {
		t.Fatalf("UpdateKimiAuth: %v", err)
	}
	got := readJSONMap(t, path)
	if got["access_token"] != "new-access" || got["refresh_token"] != "new-refresh" {
		t.Errorf("tokens = %v / %v", got["access_token"], got["refresh_token"])
	}
	expiresAt, _ := got["expires_at"].(float64)
	if int64(expiresAt) < before+3600 || int64(expiresAt) > before+3600+5 {
		t.Errorf("expires_at = %v, want about now+3600s", got["expires_at"])
	}
	if got["expires_in"] != float64(3600) {
		t.Errorf("expires_in = %v", got["expires_in"])
	}
	if got["scope"] != "kimi-code" || got["token_type"] != "Bearer" || got["extra"] != "kept" {
		t.Errorf("untouched fields changed: %v", got)
	}

	// No refresh token in the answer: the one on file stays.
	if err := UpdateKimiAuth(path, &TokenResponse{AccessToken: "newer-access", ExpiresIn: 60}); err != nil {
		t.Fatalf("UpdateKimiAuth: %v", err)
	}
	got = readJSONMap(t, path)
	if got["access_token"] != "newer-access" || got["refresh_token"] != "new-refresh" {
		t.Errorf("after a refresh-token-less answer: %v / %v", got["access_token"], got["refresh_token"])
	}
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}
