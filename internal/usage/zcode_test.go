package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/zcodecred"
)

func TestZcodeFetcher_Fetch(t *testing.T) {
	var body string
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != ZcodeBillingPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	f := NewZcodeFetcher()
	f.baseURL = server.URL

	// The live shape for an account without a coding plan.
	body = `{"code":0,"msg":"","data":{"server_time":1789325475,"plans":[]}}`
	info, err := f.Fetch(context.Background(), "SYNTHETIC-SESSION")
	if err != nil {
		t.Fatalf("Fetch(no plans) error = %v", err)
	}
	if gotAuth != "Bearer SYNTHETIC-SESSION" {
		t.Errorf("auth = %q", gotAuth)
	}
	if info.Error != ErrNoZcodePlan || info.PrimaryWindow != nil {
		t.Fatalf("no-plan row = %+v, want the explicit no-plan error and no windows", info)
	}
	if info.AvailabilityScore() != 0 {
		t.Error("a login without a plan must not score as idle capacity")
	}

	// A plan with recognisable figures.
	body = `{"code":0,"msg":"","data":{"server_time":1,"plans":[{"plan_name":"Coding Plan Pro","percentage":42,"next_reset_time":1893456000000},{"name":"Weekly","used":30,"limit":100,"reset_time":"2030-01-02T00:00:00Z"}]}}`
	info, err = f.Fetch(context.Background(), "SYNTHETIC-SESSION")
	if err != nil || info.Error != "" {
		t.Fatalf("Fetch(plans) = %+v, %v", info, err)
	}
	if info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 42 || info.PrimaryWindow.Label != "Coding Plan Pro" || info.PrimaryWindow.ResetsAt.UTC().Year() != 2030 {
		t.Errorf("primary = %+v", info.PrimaryWindow)
	}
	if info.SecondaryWindow == nil || info.SecondaryWindow.UsedPercent != 30 || info.SecondaryWindow.Label != "Weekly" {
		t.Errorf("secondary = %+v", info.SecondaryWindow)
	}

	// A plan whose fields caam does not know is named, not zeroed.
	body = `{"code":0,"msg":"","data":{"server_time":1,"plans":[{"plan_name":"Mystery","quota_left":5}]}}`
	info, _ = f.Fetch(context.Background(), "SYNTHETIC-SESSION")
	if info.PrimaryWindow != nil || !strings.Contains(info.Error, "Mystery") {
		t.Errorf("unknown plan row = %+v", info)
	}

	// The API's own error envelope.
	body = `{"code":3001,"msg":"parameter error"}`
	if info, err := f.Fetch(context.Background(), "SYNTHETIC-SESSION"); err == nil || !strings.Contains(info.Error, "3001") {
		t.Errorf("envelope error = %+v, %v", info, err)
	}
}

func TestReadZcodeCredentials(t *testing.T) {
	t.Setenv(zcodecred.SecretEnv, "unit-secret")
	path := filepath.Join(t.TempDir(), "credentials.json")
	seal := func(v string) string {
		t.Helper()
		s, err := zcodecred.EncryptWith(v, "unit-secret")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	write := func(values map[string]string) {
		t.Helper()
		data, _ := json.Marshal(values)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]string{zcodecred.KeyActiveProvider: seal("zai")})
	if _, _, err := ReadZcodeCredentials(path); err == nil || !strings.Contains(err.Error(), "logged out") {
		t.Errorf("session-less record = %v", err)
	}
	write(map[string]string{
		zcodecred.KeyJWTToken:    seal("SYNTHETIC-SESSION"),
		zcodecred.KeyAccessToken: seal("SYNTHETIC-ACCESS"),
		zcodecred.KeyUserInfo:    seal(`{"email":"dev@example.com","user_id":"u-9"}`),
	})
	// The billing API answers to the session JWT, not the Z.ai access token.
	tok, id, err := ReadCredentials("zcode", path)
	if err != nil || tok != "SYNTHETIC-SESSION" || id != "u-9" {
		t.Errorf("ReadCredentials(zcode) = %q, %q, %v", tok, id, err)
	}
	if files := CredentialFiles("zcode"); len(files) != 1 || files[0] != "credentials.json" {
		t.Errorf("CredentialFiles(zcode) = %v", files)
	}
}
