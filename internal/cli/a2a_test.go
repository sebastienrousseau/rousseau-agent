package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// -- keygen ---------------------------------------------------------

func TestA2AKeygen_ToStdout_ProducesRoundtrippableKeys(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	require.NoError(t, a2aKeygenRun(&stdout, &stderr, "", ""))

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	require.Len(t, lines, 2, "keygen must emit private + public on separate lines")

	priv, err := base64.StdEncoding.DecodeString(lines[0])
	require.NoError(t, err)
	pub, err := base64.StdEncoding.DecodeString(lines[1])
	require.NoError(t, err)
	assert.Len(t, priv, ed25519.PrivateKeySize)
	assert.Len(t, pub, ed25519.PublicKeySize)

	// Sanity: the pub must be the derivable public half of the priv.
	derivedPub := ed25519.PrivateKey(priv).Public().(ed25519.PublicKey)
	assert.Equal(t, []byte(derivedPub), pub, "emitted public key must match the private key's derived public half")
}

func TestA2AKeygen_ToFile_HonoursMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	privPath := filepath.Join(dir, "priv.key")
	pubPath := filepath.Join(dir, "pub.key")
	var stdout, stderr bytes.Buffer
	require.NoError(t, a2aKeygenRun(&stdout, &stderr, privPath, pubPath))

	assert.Empty(t, stdout.String(), "with both --*-out set, nothing lands on stdout")
	assert.Contains(t, stderr.String(), "private key")
	assert.Contains(t, stderr.String(), "public key")

	privInfo, err := os.Stat(privPath)
	require.NoError(t, err)
	pubInfo, err := os.Stat(pubPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), privInfo.Mode().Perm(), "private key file must be 0600")
	assert.Equal(t, os.FileMode(0o644), pubInfo.Mode().Perm(), "public key file must be 0644")
}

func TestA2AKeygen_WriteFailureSurfaces(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	// Point --priv-out at a directory that does not exist so os.WriteFile fails.
	err := a2aKeygenRun(&stdout, &stderr, "/no/such/dir/priv.key", "-")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write")
}

// -- sign-card ------------------------------------------------------

// writeTestCard writes a minimal AgentCard JSON blob to a temp file
// and returns its path. Shared helper for sign/verify tests.
func writeTestCard(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "card.json")
	blob, err := json.MarshalIndent(a2a.UpgradeCard(a2a.CapabilityCard{
		Name: "test-peer", Version: "v0.0.5",
	}), "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, blob, 0o600))
	return p
}

// writeTestKeypair generates a keypair, writes both halves to disk in
// the base64 format loadA2A{Public,Private}Keys expects, and returns
// the two paths + the raw ed25519 objects.
func writeTestKeypair(t *testing.T) (privPath, pubPath string, pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	rawPub, rawPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	privPath = filepath.Join(dir, "priv.key")
	pubPath = filepath.Join(dir, "pub.key")
	require.NoError(t, os.WriteFile(privPath, []byte(base64.StdEncoding.EncodeToString(rawPriv)+"\n"), 0o600))
	require.NoError(t, os.WriteFile(pubPath, []byte(base64.StdEncoding.EncodeToString(rawPub)+"\n"), 0o644))
	return privPath, pubPath, rawPub, rawPriv
}

func TestA2ASignCard_HappyPath(t *testing.T) {
	t.Parallel()
	cardPath := writeTestCard(t)
	privPath, _, pub, _ := writeTestKeypair(t)

	var stdout bytes.Buffer
	require.NoError(t, a2aSignCardRun(&stdout, cardPath, privPath, ""))

	// The signed blob should round-trip parse and verify.
	var signed a2a.AgentCard
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &signed))
	require.NotEmpty(t, signed.Signatures)
	require.NoError(t, a2a.VerifyAgentCard(signed, []ed25519.PublicKey{pub}))
}

func TestA2ASignCard_ToFile(t *testing.T) {
	t.Parallel()
	cardPath := writeTestCard(t)
	privPath, _, pub, _ := writeTestKeypair(t)
	outPath := filepath.Join(t.TempDir(), "signed.json")

	require.NoError(t, a2aSignCardRun(&bytes.Buffer{}, cardPath, privPath, outPath))

	body, err := os.ReadFile(outPath)
	require.NoError(t, err)
	var signed a2a.AgentCard
	require.NoError(t, json.Unmarshal(body, &signed))
	require.NoError(t, a2a.VerifyAgentCard(signed, []ed25519.PublicKey{pub}))
}

func TestA2ASignCard_MissingCardFails(t *testing.T) {
	t.Parallel()
	privPath, _, _, _ := writeTestKeypair(t)
	err := a2aSignCardRun(&bytes.Buffer{}, "/no/such/card.json", privPath, "-")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read")
}

func TestA2ASignCard_MalformedCardFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	badCard := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(badCard, []byte("{not-json"), 0o600))
	privPath, _, _, _ := writeTestKeypair(t)
	err := a2aSignCardRun(&bytes.Buffer{}, badCard, privPath, "-")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse card")
}

func TestA2ASignCard_BadKeyFails(t *testing.T) {
	t.Parallel()
	cardPath := writeTestCard(t)
	dir := t.TempDir()
	badKey := filepath.Join(dir, "bad.key")
	require.NoError(t, os.WriteFile(badKey, []byte("not-base64!!"), 0o600))
	err := a2aSignCardRun(&bytes.Buffer{}, cardPath, badKey, "-")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode key")
}

// -- verify-card ----------------------------------------------------

func TestA2AVerifyCard_HappyPath(t *testing.T) {
	t.Parallel()
	cardPath := writeTestCard(t)
	privPath, pubPath, _, _ := writeTestKeypair(t)

	// Sign first to produce a card the verify path can accept.
	signedPath := filepath.Join(t.TempDir(), "signed.json")
	require.NoError(t, a2aSignCardRun(&bytes.Buffer{}, cardPath, privPath, signedPath))

	var stdout, stderr bytes.Buffer
	require.NoError(t, a2aVerifyCardRun(&stdout, &stderr, signedPath, []string{pubPath}))
	assert.Contains(t, stdout.String(), "verify-card OK")
	assert.Contains(t, stdout.String(), "test-peer")
}

func TestA2AVerifyCard_UntrustedKeyFails(t *testing.T) {
	t.Parallel()
	cardPath := writeTestCard(t)
	privPath, _, _, _ := writeTestKeypair(t)
	signedPath := filepath.Join(t.TempDir(), "signed.json")
	require.NoError(t, a2aSignCardRun(&bytes.Buffer{}, cardPath, privPath, signedPath))

	// Different keypair's public key on the trust list.
	_, otherPubPath, _, _ := writeTestKeypair(t)
	var stdout, stderr bytes.Buffer
	err := a2aVerifyCardRun(&stdout, &stderr, signedPath, []string{otherPubPath})
	require.Error(t, err)
	assert.Contains(t, stderr.String(), "verify-card FAILED")
}

func TestA2AVerifyCard_UnsignedCardFailsWithSentinel(t *testing.T) {
	t.Parallel()
	cardPath := writeTestCard(t)
	_, pubPath, _, _ := writeTestKeypair(t)
	var stdout, stderr bytes.Buffer
	err := a2aVerifyCardRun(&stdout, &stderr, cardPath, []string{pubPath})
	require.Error(t, err)
	assert.ErrorIs(t, err, a2a.ErrCardUnsigned)
}

func TestA2AVerifyCard_MissingFileFails(t *testing.T) {
	t.Parallel()
	_, pubPath, _, _ := writeTestKeypair(t)
	var stdout, stderr bytes.Buffer
	err := a2aVerifyCardRun(&stdout, &stderr, "/no/such/card.json", []string{pubPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read")
}

func TestA2AVerifyCard_MalformedCardFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not-json"), 0o600))
	_, pubPath, _, _ := writeTestKeypair(t)
	var stdout, stderr bytes.Buffer
	err := a2aVerifyCardRun(&stdout, &stderr, bad, []string{pubPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse card")
}

func TestA2AVerifyCard_BadTrustKeyFails(t *testing.T) {
	t.Parallel()
	cardPath := writeTestCard(t)
	dir := t.TempDir()
	badPub := filepath.Join(dir, "bad.pub")
	require.NoError(t, os.WriteFile(badPub, []byte("junk"), 0o600))
	var stdout, stderr bytes.Buffer
	err := a2aVerifyCardRun(&stdout, &stderr, cardPath, []string{badPub})
	require.Error(t, err)
}

func TestA2AVerifyCard_WrongSizeTrustKeyFails(t *testing.T) {
	t.Parallel()
	cardPath := writeTestCard(t)
	dir := t.TempDir()
	shortPub := filepath.Join(dir, "short.pub")
	// 16 bytes ≠ 32-byte Ed25519 public key size.
	require.NoError(t, os.WriteFile(shortPub, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, 16))), 0o600))
	var stdout, stderr bytes.Buffer
	err := a2aVerifyCardRun(&stdout, &stderr, cardPath, []string{shortPub})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want")
}

func TestA2AVerifyCard_MissingTrustKeyFails(t *testing.T) {
	t.Parallel()
	cardPath := writeTestCard(t)
	var stdout, stderr bytes.Buffer
	err := a2aVerifyCardRun(&stdout, &stderr, cardPath, []string{"/no/such/pub.key"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read trusted-key")
}

// -- fetch-card -----------------------------------------------------

func TestA2AFetchCard_HappyPath_NoVerify(t *testing.T) {
	t.Parallel()
	card := a2a.UpgradeCard(a2a.CapabilityCard{Name: "live-peer", Version: "v1"})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		_ = json.NewEncoder(w).Encode(card) //nolint:errcheck // test server
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	require.NoError(t, a2aFetchCardRun(context.Background(), &stdout, &stderr, ts.URL, nil))
	assert.Contains(t, stdout.String(), `"name": "live-peer"`)
}

func TestA2AFetchCard_WithTrustList_Verifies(t *testing.T) {
	t.Parallel()
	// Serve a signed card; verify against the signer's public key.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "pub.key")
	pub := priv.Public().(ed25519.PublicKey)
	require.NoError(t, os.WriteFile(pubPath, []byte(base64.StdEncoding.EncodeToString(pub)), 0o644))

	card := a2a.UpgradeCard(a2a.CapabilityCard{Name: "signed-peer", Version: "v1"})
	signed, err := a2a.SignAgentCard(card, priv)
	require.NoError(t, err)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		_ = json.NewEncoder(w).Encode(signed) //nolint:errcheck // test server
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	require.NoError(t, a2aFetchCardRun(context.Background(), &stdout, &stderr, ts.URL, []string{pubPath}))
	assert.Contains(t, stderr.String(), "signature verified")
	assert.Contains(t, stdout.String(), `"name": "signed-peer"`)
}

func TestA2AFetchCard_WithTrustList_RejectsUntrusted(t *testing.T) {
	t.Parallel()
	// Serve an unsigned card; verify fails with ErrCardUnsigned.
	dir := t.TempDir()
	_, pub, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_ = pub
	pubPath := filepath.Join(dir, "pub.key")
	// Any 32-byte value works — we don't actually need a real matching key.
	require.NoError(t, os.WriteFile(pubPath, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, ed25519.PublicKeySize))), 0o644))

	card := a2a.UpgradeCard(a2a.CapabilityCard{Name: "unsigned-peer", Version: "v1"})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", a2a.ContentTypeSpec)
		_ = json.NewEncoder(w).Encode(card) //nolint:errcheck // test server
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	err = a2aFetchCardRun(context.Background(), &stdout, &stderr, ts.URL, []string{pubPath})
	require.Error(t, err)
	assert.Contains(t, stderr.String(), "SIGNATURE VERIFY FAILED")
}

func TestA2AFetchCard_HTTPErrorSurfaces(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("down for maintenance")) //nolint:errcheck // test server
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	err := a2aFetchCardRun(context.Background(), &stdout, &stderr, ts.URL, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
}

func TestA2AFetchCard_MalformedJSONFails(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not-json")) //nolint:errcheck // test server
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	err := a2aFetchCardRun(context.Background(), &stdout, &stderr, ts.URL, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestA2AFetchCard_UnreachablePeerFails(t *testing.T) {
	t.Parallel()
	// Point at a closed port with a short-lived context.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	err := a2aFetchCardRun(ctx, &stdout, &stderr, "http://127.0.0.1:1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch-card")
}

// -- subcommand wiring ----------------------------------------------

func TestNewA2ACmd_RegistersSubcommands(t *testing.T) {
	t.Parallel()
	cmd := newA2ACmd(&Options{})
	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Use] = true
	}
	for _, want := range []string{"keygen", "sign-card", "verify-card", "fetch-card <peer-url>"} {
		assert.True(t, names[want], "missing subcommand: %s (have %v)", want, names)
	}
}

func TestSignCardCmd_FlagsRequired(t *testing.T) {
	t.Parallel()
	cmd := newA2ASignCardCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestVerifyCardCmd_FlagsRequired(t *testing.T) {
	t.Parallel()
	cmd := newA2AVerifyCardCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	// Missing --card
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--card")

	// Card set but no trust list
	require.NoError(t, cmd.Flags().Set("card", "/dev/null"))
	err = cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trusted-key")
}
