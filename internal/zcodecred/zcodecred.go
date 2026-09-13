// Package zcodecred reads the credential record zcode (Z.ai's coding
// harness) keeps at ~/.zcode/v2/credentials.json.
//
// Every value in that file is sealed with AES-256-GCM under a key derived
// from a per-user secret: ZCODE_CREDENTIAL_SECRET when set, otherwise the
// string "zcode-credential-fallback:<platform>:<homedir>:<username>" — a
// deterministic secret, so the file is portable between processes of the
// same user on the same machine and opaque to anyone else. A sealed value
// reads "enc:v1:<iv>.<tag>.<ciphertext>" with each part base64url-encoded
// (no padding); a value without the prefix is plain.
//
// The record holds the active OAuth provider, the Z.ai access token (a
// short-lived JWT), the zcode session JWT that zcode's own billing API
// accepts, and the signed-in user's profile. caam decrypts only to read
// identity and present a token to a usage API; the file itself is captured
// and restored verbatim, since it is what zcode reads back.
package zcodecred

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// Prefix marks a sealed value.
const Prefix = "enc:v1:"

// SecretEnv is the environment variable zcode reads its credential secret
// from before falling back to the per-user default.
const SecretEnv = "ZCODE_CREDENTIAL_SECRET"

// DataBaseDirEnv relocates zcode's data directory (the parent of .zcode).
const DataBaseDirEnv = "ZCODE_DATA_BASE_DIR"

// Keys of the credential record.
const (
	KeyActiveProvider = "oauth:active_provider"
	KeyAccessToken    = "oauth:zai:access_token"
	KeyRefreshToken   = "oauth:zai:refresh_token"
	KeyUserInfo       = "oauth:zai:user_info"
	KeyJWTToken       = "zcodejwttoken"
)

// ErrNoRecord means the credentials file does not exist.
var ErrNoRecord = errors.New("zcode credentials not found")

// DefaultPath is where zcode keeps the record: <ZCODE_DATA_BASE_DIR or
// HOME>/.zcode/v2/credentials.json.
func DefaultPath() string {
	base := strings.TrimSpace(os.Getenv(DataBaseDirEnv))
	if base == "" {
		base, _ = os.UserHomeDir()
	}
	return filepath.Join(base, ".zcode", "v2", "credentials.json")
}

// nodePlatform is what Node's process.platform reports, which is the value
// zcode folds into the fallback secret.
func nodePlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	default:
		return runtime.GOOS
	}
}

// Secret returns the credential secret for this process: the environment
// override, or the per-user fallback zcode derives.
func Secret() string {
	if s := strings.TrimSpace(os.Getenv(SecretEnv)); s != "" {
		return s
	}
	home, _ := os.UserHomeDir()
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	return fmt.Sprintf("zcode-credential-fallback:%s:%s:%s", nodePlatform(), home, name)
}

func deriveKey(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// IsSealed reports whether a value carries the envelope.
func IsSealed(value string) bool {
	return strings.HasPrefix(value, Prefix)
}

// DecryptWith unseals value under secret; a plain value is returned as-is.
func DecryptWith(value, secret string) (string, error) {
	if !IsSealed(value) {
		return value, nil
	}
	parts := strings.Split(strings.TrimPrefix(value, Prefix), ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", errors.New("zcode credential: invalid ciphertext format")
	}
	iv, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("zcode credential: decode iv: %w", err)
	}
	tag, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("zcode credential: decode auth tag: %w", err)
	}
	ct, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("zcode credential: decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(deriveKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(iv) != gcm.NonceSize() {
		return "", errors.New("zcode credential: invalid IV length")
	}
	if len(tag) != gcm.Overhead() {
		return "", errors.New("zcode credential: invalid auth tag length")
	}
	plain, err := gcm.Open(nil, iv, append(ct, tag...), nil)
	if err != nil {
		return "", errors.New("zcode credential: key mismatch or corrupted ciphertext")
	}
	return string(plain), nil
}

// Decrypt unseals value under this process's secret.
func Decrypt(value string) (string, error) {
	return DecryptWith(value, Secret())
}

// EncryptWith seals plain under secret in zcode's envelope. caam never
// writes sealed values itself; this exists so tests can build fixtures the
// way zcode would.
func EncryptWith(plain, secret string) (string, error) {
	block, err := aes.NewCipher(deriveKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, []byte(plain), nil)
	ct, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	return Prefix + base64.RawURLEncoding.EncodeToString(iv) + "." +
		base64.RawURLEncoding.EncodeToString(tag) + "." +
		base64.RawURLEncoding.EncodeToString(ct), nil
}

// UserInfo is the signed-in Z.ai user, as zcode records it.
type UserInfo struct {
	Email  string `json:"email"`
	Name   string `json:"name"`
	UserID string `json:"user_id"`
	Avatar string `json:"avatar"`
}

// Record is the decrypted credential record. A key absent from the file
// leaves its field empty.
type Record struct {
	ActiveProvider string
	AccessToken    string
	RefreshToken   string
	JWTToken       string
	UserInfo       *UserInfo
}

// ReadRecord reads and unseals the record at path.
func ReadRecord(path string) (*Record, error) {
	return ReadRecordWith(path, Secret())
}

// ReadRecordWith is ReadRecord under an explicit secret.
func ReadRecordWith(path, secret string) (*Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoRecord
		}
		return nil, err
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse zcode credentials: %w", err)
	}
	get := func(key string) (string, error) {
		v, ok := raw[key]
		if !ok || v == "" {
			return "", nil
		}
		return DecryptWith(v, secret)
	}
	rec := &Record{}
	var firstErr error
	keep := func(dst *string, key string) {
		v, err := get(key)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", key, err)
		}
		*dst = v
	}
	keep(&rec.ActiveProvider, KeyActiveProvider)
	keep(&rec.AccessToken, KeyAccessToken)
	keep(&rec.RefreshToken, KeyRefreshToken)
	keep(&rec.JWTToken, KeyJWTToken)
	if info, err := get(KeyUserInfo); err != nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", KeyUserInfo, err)
		}
	} else if info != "" {
		var ui UserInfo
		if err := json.Unmarshal([]byte(info), &ui); err == nil {
			rec.UserInfo = &ui
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return rec, nil
}

// LoggedIn reports whether the record carries a usable login: a session
// JWT or an access token.
func (r *Record) LoggedIn() bool {
	return r != nil && (r.JWTToken != "" || r.AccessToken != "")
}
