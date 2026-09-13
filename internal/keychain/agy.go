package keychain

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// The Antigravity CLI (agy) keeps its Google OAuth token in the login
// keychain as a generic password with service "gemini" and account
// "antigravity", written through the go-keyring library. On Linux the same
// bytes live in ~/.gemini/antigravity-cli/antigravity-oauth-token, which is
// the file caam's agy file set names; on a Mac that file does not exist and
// the item is bridged onto it exactly as Claude Code's is (issue #98).
const (
	AgyService = "gemini"
	AgyAccount = "antigravity"
)

// go-keyring cannot hand `security` a value with surrounding whitespace (the
// CLI trims it) or non-ASCII bytes, so it wraps such values in one of two
// self-describing envelopes and unwraps them on read. caam must do the same
// in both directions or agy reads back a token it cannot parse.
const (
	goKeyringBase64Prefix = "go-keyring-base64:"
	goKeyringHexPrefix    = "go-keyring-encoded:"
)

// decodeGoKeyring unwraps a go-keyring envelope; a bare value is returned
// as-is.
func decodeGoKeyring(raw []byte) ([]byte, error) {
	s := string(raw)
	switch {
	case strings.HasPrefix(s, goKeyringBase64Prefix):
		out, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, goKeyringBase64Prefix))
		if err != nil {
			return nil, fmt.Errorf("keychain: decode go-keyring base64 envelope: %w", err)
		}
		return out, nil
	case strings.HasPrefix(s, goKeyringHexPrefix):
		out, err := hex.DecodeString(strings.TrimPrefix(s, goKeyringHexPrefix))
		if err != nil {
			return nil, fmt.Errorf("keychain: decode go-keyring hex envelope: %w", err)
		}
		return out, nil
	}
	return raw, nil
}

// encodeGoKeyring wraps blob in the base64 envelope. go-keyring unwraps
// either envelope (and passes a bare value through) on every read, so the
// choice only has to be one agy accepts — and base64 is the form the live
// item was observed in, so a restored token is stored exactly the way a
// fresh login stores it. Base64 also carries any byte, which the bare form
// cannot (`security` trims surrounding whitespace and rejects non-ASCII).
func encodeGoKeyring(blob []byte) []byte {
	if len(blob) == 0 {
		return blob
	}
	return []byte(goKeyringBase64Prefix + base64.StdEncoding.EncodeToString(blob))
}

// ReadAgy returns the Antigravity token item, unwrapped from go-keyring's
// envelope: the same bytes agy would write to antigravity-oauth-token on a
// platform without a keychain.
func ReadAgy() ([]byte, error) {
	raw, err := Get(AgyService, AgyAccount)
	if err != nil {
		debugf("Antigravity item not read", "service", AgyService, "account", AgyAccount, "error", err)
		return nil, err
	}
	blob, err := decodeGoKeyring(raw)
	if err != nil {
		debugf("Antigravity item envelope not decoded", "service", AgyService, "account", AgyAccount, "bytes", len(raw))
		return nil, err
	}
	if len(bytes.TrimSpace(blob)) == 0 {
		debugf("Antigravity item is empty", "service", AgyService, "account", AgyAccount)
		return nil, fmt.Errorf("keychain: item %q/%q is empty", AgyService, AgyAccount)
	}
	debugf("Antigravity item read", "service", AgyService, "account", AgyAccount, "bytes", len(blob), "enveloped", len(blob) != len(raw))
	return blob, nil
}

// WriteAgy stores blob as the Antigravity token item, in the envelope agy
// itself expects to unwrap.
func WriteAgy(blob []byte) error {
	if len(bytes.TrimSpace(blob)) == 0 {
		return errors.New("keychain: refusing to store an empty Antigravity token")
	}
	return Set(AgyService, AgyAccount, encodeGoKeyring(blob))
}

// DeleteAgy removes the Antigravity token item. A missing item, or no
// keychain at all, is not an error.
func DeleteAgy() error {
	ForgetMirrors()
	return Delete(AgyService, AgyAccount)
}

// EnsureAgyMirror refreshes tokenPath from the keychain item, so the
// file-shaped code paths (hashing, backup, expiry) see the token in force.
// ErrNoKeychain and ErrNotFound mean "nothing to bridge here".
func EnsureAgyMirror(tokenPath string) (bool, error) {
	return ensureMirrorFrom(tokenPath, ReadAgy)
}

// PushAgyMirror writes tokenPath's contents back into the keychain item,
// making the restored profile the account agy will actually use.
func PushAgyMirror(tokenPath string) error {
	if !Enabled() {
		return ErrNoKeychain
	}
	blob, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("keychain: read %s: %w", tokenPath, err)
	}
	forgetMirror(tokenPath)
	// The bytes are stored verbatim (envelope aside): agy wrote them with a
	// trailing newline and unwraps exactly what it wrote.
	return WriteAgy(blob)
}
