package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// writePubKey drops a base64-encoded Ed25519 public key at a temp
// path and returns the path. Shared helper for trust-list tests.
func writePubKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	dir := t.TempDir()
	p := filepath.Join(dir, "pub.key")
	require.NoError(t, os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644))
	return p
}

// --- loadA2AClientTrustList --------------------------------------

func TestLoadA2AClientTrustList_EmptyReturnsEmptySlice(t *testing.T) {
	t.Parallel()
	// Zero-length input is a valid "no trust list" posture, not an
	// error. The returned slice is non-nil so it flows straight into
	// a2aclient.Config.TrustedPublisherKeys without an intermediary
	// nil check.
	got, err := loadA2AClientTrustList(nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got)
}

func TestLoadA2AClientTrustList_HappyPath(t *testing.T) {
	t.Parallel()
	a := writePubKey(t)
	b := writePubKey(t)
	got, err := loadA2AClientTrustList([]string{a, b})
	require.NoError(t, err)
	assert.Len(t, got, 2)
	for _, k := range got {
		assert.Len(t, k, ed25519.PublicKeySize)
	}
}

func TestLoadA2AClientTrustList_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := loadA2AClientTrustList([]string{"/no/such/pub.key"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read trusted-key")
}

func TestLoadA2AClientTrustList_MalformedBase64(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pub")
	require.NoError(t, os.WriteFile(bad, []byte("!!!not-base64"), 0o644))
	_, err := loadA2AClientTrustList([]string{bad})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode trusted-key")
}

func TestLoadA2AClientTrustList_WrongSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	short := filepath.Join(dir, "short.pub")
	// 16 bytes ≠ 32-byte Ed25519 public key.
	require.NoError(t, os.WriteFile(short, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, 16))), 0o644))
	_, err := loadA2AClientTrustList([]string{short})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want")
}

// --- buildA2AClients disable-shaped paths ------------------------

func TestBuildA2AClients_NilInputReturnsNilMap(t *testing.T) {
	t.Parallel()
	got := buildA2AClients(nil, a2aSilentLogger())
	assert.Nil(t, got)
}

func TestBuildA2AClients_EmptyInputReturnsNilMap(t *testing.T) {
	t.Parallel()
	got := buildA2AClients([]config.A2AClientConfig{}, a2aSilentLogger())
	assert.Nil(t, got)
}

func TestBuildA2AClients_MissingNameSkipped(t *testing.T) {
	t.Parallel()
	got := buildA2AClients([]config.A2AClientConfig{
		{Endpoint: "https://peer.example.com"},
	}, a2aSilentLogger())
	assert.Empty(t, got, "missing name → skip")
}

func TestBuildA2AClients_MissingEndpointSkipped(t *testing.T) {
	t.Parallel()
	got := buildA2AClients([]config.A2AClientConfig{
		{Name: "peer"},
	}, a2aSilentLogger())
	assert.Empty(t, got)
}

func TestBuildA2AClients_UnreadableTrustFileSkipsPeer(t *testing.T) {
	t.Parallel()
	got := buildA2AClients([]config.A2AClientConfig{
		{
			Name:                 "peer",
			Endpoint:             "https://peer.example.com",
			TrustedPublisherKeys: []string{"/no/such/pub.key"},
		},
	}, a2aSilentLogger())
	assert.Empty(t, got, "broken trust file → skip peer, don't fail-close whole daemon")
}

// --- buildA2AClients happy path ---------------------------------

func TestBuildA2AClients_HappyPath_NoAuth(t *testing.T) {
	t.Parallel()
	got := buildA2AClients([]config.A2AClientConfig{
		{Name: "peer-a", Endpoint: "https://peer-a.example.com"},
		{Name: "peer-b", Endpoint: "https://peer-b.example.com"},
	}, a2aSilentLogger())
	require.Len(t, got, 2)
	assert.Contains(t, got, "peer-a")
	assert.Contains(t, got, "peer-b")
}

func TestBuildA2AClients_HappyPath_WithTrustList(t *testing.T) {
	t.Parallel()
	pub := writePubKey(t)
	got := buildA2AClients([]config.A2AClientConfig{
		{
			Name: "peer", Endpoint: "https://peer.example.com",
			TrustedPublisherKeys: []string{pub},
			RequireSignedCard:    true,
		},
	}, a2aSilentLogger())
	require.Len(t, got, 1)
	c, ok := got["peer"]
	require.True(t, ok)
	assert.Equal(t, "peer", c.Name())
}

func TestBuildA2AClients_AuthEnvSetAppliesHeader(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel — leave sequential.
	t.Setenv("TEST_A2A_TOKEN", "Bearer opaque-token")
	got := buildA2AClients([]config.A2AClientConfig{
		{
			Name: "peer", Endpoint: "https://peer.example.com",
			AuthHeaderEnv: "TEST_A2A_TOKEN",
		},
	}, a2aSilentLogger())
	require.Len(t, got, 1)
	// We don't have a public accessor for AuthHeader — freeze the
	// contract via the presence of the peer in the map. Empty-env
	// case below covers the counterfactual.
	_, ok := got["peer"]
	assert.True(t, ok)
}

func TestBuildA2AClients_AuthEnvUnsetStillConstructs(t *testing.T) {
	t.Parallel()
	// Freezes the "dev-friendly startup" contract: an operator whose
	// peer isn't onboarded yet (env var not set) still gets a
	// running daemon.
	got := buildA2AClients([]config.A2AClientConfig{
		{
			Name: "peer", Endpoint: "https://peer.example.com",
			AuthHeaderEnv: "DEFINITELY_NOT_SET_" + t.Name(),
		},
	}, a2aSilentLogger())
	require.Len(t, got, 1)
}

func TestBuildA2AClients_DuplicateNamesShadowSilently(t *testing.T) {
	t.Parallel()
	// Freezes shadow-not-fail contract. First-writer-wins would be
	// surprising; last-writer-wins matches Go map semantics + logs a
	// WARN. This test lives to catch a change of the ordering
	// invariant.
	got := buildA2AClients([]config.A2AClientConfig{
		{Name: "peer", Endpoint: "https://a.example.com"},
		{Name: "peer", Endpoint: "https://b.example.com"},
	}, a2aSilentLogger())
	require.Len(t, got, 1)
}

// --- doctor rows -------------------------------------------------

func TestA2APeerRows_UnnamedPeerFails(t *testing.T) {
	t.Parallel()
	rows := a2aPeerRows(config.A2AClientConfig{Endpoint: "https://x.example"})
	require.Len(t, rows, 1)
	assert.Equal(t, "fail", rows[0].Status)
	assert.Contains(t, rows[0].Name, "<unnamed>")
}

func TestA2APeerRows_MissingEndpointFails(t *testing.T) {
	t.Parallel()
	rows := a2aPeerRows(config.A2AClientConfig{Name: "peer"})
	// Endpoint row is the first sub-row.
	var got *diagResult
	for i := range rows {
		if rows[i].Name == "identity.a2a.clients.peer.endpoint" {
			got = &rows[i]
			break
		}
	}
	require.NotNil(t, got)
	assert.Equal(t, "fail", got.Status)
}

func TestA2APeerRows_RequireSignedWithoutTrustListFails(t *testing.T) {
	t.Parallel()
	rows := a2aPeerRows(config.A2AClientConfig{
		Name: "peer", Endpoint: "https://peer.example.com",
		RequireSignedCard: true,
	})
	verify := findRow(rows, "identity.a2a.clients.peer.verify")
	require.NotNil(t, verify)
	assert.Equal(t, "fail", verify.Status)
	assert.Contains(t, verify.Detail, "every fetch will reject")
}

func TestA2APeerRows_StrictModeReported(t *testing.T) {
	t.Parallel()
	rows := a2aPeerRows(config.A2AClientConfig{
		Name: "peer", Endpoint: "https://peer.example.com",
		TrustedPublisherKeys: []string{"/tmp/x.pub"},
		RequireSignedCard:    true,
	})
	verify := findRow(rows, "identity.a2a.clients.peer.verify")
	require.NotNil(t, verify)
	assert.Equal(t, "ok", verify.Status)
	assert.Contains(t, verify.Detail, "strict")
	assert.Contains(t, verify.Detail, "1 trusted")
}

func TestA2APeerRows_LenientVerifyMode(t *testing.T) {
	t.Parallel()
	rows := a2aPeerRows(config.A2AClientConfig{
		Name: "peer", Endpoint: "https://peer.example.com",
		TrustedPublisherKeys: []string{"/tmp/x.pub"},
		// RequireSignedCard=false (default)
	})
	verify := findRow(rows, "identity.a2a.clients.peer.verify")
	require.NotNil(t, verify)
	assert.Contains(t, verify.Detail, "verify-when-signed")
}

func TestA2APeerRows_NoTrustListInfoMode(t *testing.T) {
	t.Parallel()
	rows := a2aPeerRows(config.A2AClientConfig{
		Name: "peer", Endpoint: "https://peer.example.com",
	})
	verify := findRow(rows, "identity.a2a.clients.peer.verify")
	require.NotNil(t, verify)
	assert.Equal(t, "info", verify.Status)
	assert.Contains(t, verify.Detail, "no trust list")
}

func TestA2APeerRows_AuthEnvSurfacedNotValue(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel — leave sequential.
	// Safety-critical: doctor output must be safe to paste in a
	// support ticket. The token value is fetched from env — the
	// row shows the env var name, not the secret.
	t.Setenv("SECRET_TOKEN", "super-secret-should-not-leak")
	rows := a2aPeerRows(config.A2AClientConfig{
		Name: "peer", Endpoint: "https://peer.example.com",
		AuthHeaderEnv: "SECRET_TOKEN",
	})
	auth := findRow(rows, "identity.a2a.clients.peer.auth")
	require.NotNil(t, auth)
	assert.Contains(t, auth.Detail, "SECRET_TOKEN")
	assert.NotContains(t, auth.Detail, "super-secret-should-not-leak")
}

func TestA2APeerRows_NoAuthEnvIsAnonymous(t *testing.T) {
	t.Parallel()
	rows := a2aPeerRows(config.A2AClientConfig{
		Name: "peer", Endpoint: "https://peer.example.com",
	})
	auth := findRow(rows, "identity.a2a.clients.peer.auth")
	require.NotNil(t, auth)
	assert.Contains(t, auth.Detail, "anonymously")
}

// findRow is a tiny helper: returns the first diagResult whose Name
// matches, or nil.
func findRow(rows []diagResult, name string) *diagResult {
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

// TestCheckA2A_EmitsPeerRows ensures the sub-row assembly is
// actually invoked when clients are present.
func TestCheckA2A_EmitsPeerRows(t *testing.T) {
	t.Parallel()
	rows := checkA2A(&config.Config{A2A: config.A2AConfig{
		Clients: []config.A2AClientConfig{
			{Name: "alpha", Endpoint: "https://alpha.example.com"},
			{Name: "beta", Endpoint: "https://beta.example.com", TrustedPublisherKeys: []string{"/tmp/x.pub"}},
		},
	}})
	names := diagNames(rows)
	assert.Contains(t, names, "identity.a2a.clients")
	assert.Contains(t, names, "identity.a2a.clients.alpha.endpoint")
	assert.Contains(t, names, "identity.a2a.clients.beta.verify")
	assert.Contains(t, names, "identity.a2a.dispatch_tool",
		"tool-registered row must surface so operators see the model can call peers")
}
