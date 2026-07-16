package oauth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EnvMasterKey is the environment variable read for the hex-encoded
// master key. Highest priority in [ResolveMasterKey].
const EnvMasterKey = "ROUSSEAU_TOKEN_KEY"

// ResolveMasterKey returns the AEAD master key using this precedence:
//
//  1. $ROUSSEAU_TOKEN_KEY as a 64-char hex string.
//  2. $XDG_STATE_HOME/rousseau/token.key (or ~/.local/state/rousseau/token.key
//     when XDG_STATE_HOME is unset). Mode 0600 is enforced; a wider
//     mode is treated as an error.
//
// If neither source has a key and generateIfMissing is true, a fresh
// key is generated and persisted to the file path. This is the
// solo-user first-run path; enterprise operators are expected to
// supply $ROUSSEAU_TOKEN_KEY via a systemd-credential or vault, in
// which case generateIfMissing is a no-op because #1 fires.
//
// Callers that want to run without persisting a key at all should
// use [GenerateKey] directly.
func ResolveMasterKey(generateIfMissing bool) ([]byte, error) {
	if envHex := os.Getenv(EnvMasterKey); envHex != "" {
		k, err := DecodeHexKey(envHex)
		if err != nil {
			return nil, fmt.Errorf("oauth: parse $%s: %w", EnvMasterKey, err)
		}
		return k, nil
	}
	path, err := keyFilePath()
	if err != nil {
		return nil, err
	}
	if raw, ok, err := readKeyFile(path); err != nil {
		return nil, err
	} else if ok {
		return raw, nil
	}
	if !generateIfMissing {
		return nil, errors.New("oauth: no master key found (set $" + EnvMasterKey + ")")
	}
	k, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := writeKeyFile(path, k); err != nil {
		return nil, err
	}
	return k, nil
}

// keyFilePath resolves $XDG_STATE_HOME/rousseau/token.key with the
// portable default.
func keyFilePath() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("oauth: home dir: %w", err)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "rousseau", "token.key"), nil
}

// readKeyFile reads the hex-encoded key at path. Returns
// (nil, false, nil) if the file does not exist.
func readKeyFile(path string) ([]byte, bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("oauth: stat key file: %w", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		return nil, false, fmt.Errorf("oauth: key file %s has mode %o, expected 0600 (chmod 600 the file)", path, perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("oauth: read key file: %w", err)
	}
	k, err := DecodeHexKey(string(raw))
	if err != nil {
		return nil, false, fmt.Errorf("oauth: parse key file: %w", err)
	}
	return k, true, nil
}

// writeKeyFile persists the hex-encoded key at path with mode 0600
// and creates parent directories with mode 0700 if missing.
func writeKeyFile(path string, key []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("oauth: create key dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(EncodeHexKey(key)), 0o600); err != nil {
		return fmt.Errorf("oauth: write key file: %w", err)
	}
	return nil
}
