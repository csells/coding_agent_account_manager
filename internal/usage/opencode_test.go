package usage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeFetcher_Fetch(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"usage":{"rolling":{"percent":12.5,"resetInSec":3600},"weekly":{"percent":40,"resetInSec":86400},"monthly":{"percent":1,"renewAt":"2030-02-01T00:00:00Z"}}}`)
	}))
	defer server.Close()
	f := NewOpenCodeFetcher()
	f.url = server.URL
	info, err := f.Fetch(context.Background(), "SYNTHETIC-ZEN")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "Bearer SYNTHETIC-ZEN" {
		t.Errorf("auth = %q", gotAuth)
	}
	if info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 13 || info.PrimaryWindow.ResetsAt.IsZero() {
		t.Errorf("primary = %+v", info.PrimaryWindow)
	}
	if info.SecondaryWindow == nil || info.SecondaryWindow.UsedPercent != 40 {
		t.Errorf("secondary = %+v", info.SecondaryWindow)
	}
	if info.ModelWindows["monthly"] == nil || info.ModelWindows["monthly"].UsedPercent != 1 {
		t.Errorf("monthly = %+v", info.ModelWindows)
	}

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer denied.Close()
	f.url = denied.URL
	if info, err := f.Fetch(context.Background(), "bad"); err == nil || !strings.HasPrefix(info.Error, "unauthorized") {
		t.Errorf("401 = %+v, %v", info, err)
	}
}

func TestReadOpenCodeCredentials(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// A Console login with no Zen key: identity known, no limits API.
	noKey := write("nokey.json", `{"tables":{"account":[{"id":"acc_1","email":"a@example.com"}],"account_state":[{"id":1,"active_account_id":"acc_1"}],"credential":[{"id":"c1","integration_id":"anthropic","method_id":"api","value":"sk-ant-SYNTHETIC"}]}}`)
	tok, id, err := ReadOpenCodeCredentials(noKey)
	if err == nil || err.Error() != ErrNoOpenCodeLimitsAPI || tok != "" || id != "a@example.com" {
		t.Errorf("no-key export = %q, %q, %v", tok, id, err)
	}
	withKey := write("key.json", `{"tables":{"account":[{"id":"acc_1","email":"a@example.com"}],"credential":[{"id":"c2","integration_id":"opencode","method_id":"api","value":"{\"type\":\"api\",\"key\":\"SYNTHETIC-ZEN\"}"}]}}`)
	if tok, id, err := ReadCredentials("opencode", withKey); err != nil || tok != "SYNTHETIC-ZEN" || id != "a@example.com" {
		t.Errorf("keyed export = %q, %q, %v", tok, id, err)
	}
	// A Console OAuth credential is a session, not a Zen key.
	oauth := write("oauth.json", `{"tables":{"credential":[{"id":"c3","integration_id":"opencode","method_id":"device","value":"{\"type\":\"oauth\",\"access\":\"x\"}"}]}}`)
	if _, _, err := ReadOpenCodeCredentials(oauth); err == nil {
		t.Error("an OAuth credential was taken for a Zen key")
	}
	legacy := write("auth.json", `{"opencode":{"type":"api","key":"SYNTHETIC-LEGACY"},"anthropic":{"type":"api","key":"x"}}`)
	if tok, _, err := ReadOpenCodeCredentials(legacy); err != nil || tok != "SYNTHETIC-LEGACY" {
		t.Errorf("legacy auth.json = %q, %v", tok, err)
	}
	if files := CredentialFiles("opencode"); len(files) != 2 || files[0] != "opencode-auth.json" || files[1] != "auth.json" {
		t.Errorf("CredentialFiles(opencode) = %v", files)
	}
}
