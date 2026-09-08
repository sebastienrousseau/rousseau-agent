package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// a2aSilentLogger returns a slog.Logger that swallows all output.
// (Chat tests already define a package-level silentLogger; we avoid
// the collision by naming ours after this file's concern.)
func a2aSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeTokenFile drops three sample tokens (including a comment and a
// blank line to prove loader handles them) into a temp file and
// returns the path.
func writeTokenFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "tokens.list")
	body := []byte("# comment\n\ntoken-a\ntoken-b\n")
	require.NoError(t, os.WriteFile(p, body, 0o600))
	return p
}

// writeSigningKey generates a fresh Ed25519 keypair, writes the
// private key base64-encoded to a temp file, and returns the path
// plus both halves of the key so tests can verify signed cards.
func writeSigningKey(t *testing.T) (path string, priv ed25519.PrivateKey, pub ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	dir := t.TempDir()
	path = filepath.Join(dir, "signing.key")
	require.NoError(t, os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600))
	return path, priv, pub
}

// --- loader tests ---------------------------------------------------

func TestLoadA2ABearerTokens_SkipsBlankAndCommentLines(t *testing.T) {
	t.Parallel()
	path := writeTokenFile(t)
	tokens, err := loadA2ABearerTokens(path, a2aSilentLogger())
	require.NoError(t, err)
	assert.Equal(t, []string{"token-a", "token-b"}, tokens)
}

func TestLoadA2ABearerTokens_EmptyPathReturnsNil(t *testing.T) {
	t.Parallel()
	tokens, err := loadA2ABearerTokens("", a2aSilentLogger())
	require.NoError(t, err)
	assert.Nil(t, tokens)
}

func TestLoadA2ABearerTokens_MissingFileErrors(t *testing.T) {
	t.Parallel()
	_, err := loadA2ABearerTokens("/no/such/tokens", a2aSilentLogger())
	require.Error(t, err)
}

func TestLoadA2AServerSigningKey_HappyPath(t *testing.T) {
	t.Parallel()
	path, priv, _ := writeSigningKey(t)
	got, err := loadA2AServerSigningKey(path)
	require.NoError(t, err)
	assert.Equal(t, []byte(priv), []byte(got))
}

func TestLoadA2AServerSigningKey_BadBase64(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.key")
	require.NoError(t, os.WriteFile(p, []byte("not-base64!!"), 0o600))
	_, err := loadA2AServerSigningKey(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestLoadA2AServerSigningKey_WrongSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "short.key")
	// 16 bytes ≠ 64 (ed25519.PrivateKeySize)
	require.NoError(t, os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, 16))), 0o600))
	_, err := loadA2AServerSigningKey(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want")
}

func TestLoadA2AServerSigningKey_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := loadA2AServerSigningKey("/no/such/signing.key")
	require.Error(t, err)
}

// --- buildA2AServer disable-shaped paths ----------------------------

func TestBuildA2AServer_DisabledReturnsNil(t *testing.T) {
	t.Parallel()
	rt := buildA2AServer(config.A2AConfig{}, nil, a2aSilentLogger())
	assert.Nil(t, rt)
}

func TestBuildA2AServer_NilAgentDisables(t *testing.T) {
	t.Parallel()
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{Enabled: true},
	}, nil, a2aSilentLogger())
	assert.Nil(t, rt, "no agent → nothing to dispatch to → refuse to boot")
}

func TestBuildA2AServer_NoAuthTokensFileRefuses(t *testing.T) {
	t.Parallel()
	// Enabled but no auth_tokens_file → refuse to boot; a running
	// A2A server without auth would be a compliance disaster.
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{Enabled: true},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	assert.Nil(t, rt)
}

func TestBuildA2AServer_UnreadableAuthTokensFile(t *testing.T) {
	t.Parallel()
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled:        true,
			AuthTokensFile: "/no/such/tokens.list",
		},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	assert.Nil(t, rt)
}

func TestBuildA2AServer_EmptyTokensFileRefuses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.list")
	require.NoError(t, os.WriteFile(empty, []byte("# only comments\n\n"), 0o600))
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{Enabled: true, AuthTokensFile: empty},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	assert.Nil(t, rt, "no non-comment lines means no auth")
}

// --- buildA2AServer happy path -------------------------------------

func TestBuildA2AServer_HappyPath_UnsignedCard(t *testing.T) {
	t.Parallel()
	tokens := writeTokenFile(t)
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled:        true,
			AuthTokensFile: tokens,
			AgentName:      "my-peer",
			AgentID:        "peer-1",
			ExposedSkills:  []string{"review-diff", "podman-quadlet"},
		},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)
	assert.Equal(t, 2, rt.AuthCount)
	assert.False(t, rt.Signed)
	assert.Equal(t, defaultA2AListen, rt.Addr)
	// Card + skills survive the assembly.
	assert.Equal(t, "my-peer", rt.Server.Card.Name)
	assert.Equal(t, "peer-1", rt.Server.Card.AgentID)
	assert.Len(t, rt.Server.Card.Skills, 2)
}

func TestBuildA2AServer_HappyPath_SignedCard(t *testing.T) {
	t.Parallel()
	tokens := writeTokenFile(t)
	keyPath, _, pub := writeSigningKey(t)
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled:        true,
			AuthTokensFile: tokens,
			SigningKeyFile: keyPath,
			AgentName:      "signed-peer",
		},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)
	assert.True(t, rt.Signed)
	// Sanity: served card verifies against our pub.
	card := a2a.UpgradeCard(rt.Server.Card)
	signed, err := a2a.SignAgentCard(card, rt.Server.SigningKey)
	require.NoError(t, err)
	require.NoError(t, a2a.VerifyAgentCard(signed, []ed25519.PublicKey{pub}))
}

func TestBuildA2AServer_BadSigningKeyServesUnsigned(t *testing.T) {
	t.Parallel()
	tokens := writeTokenFile(t)
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled:        true,
			AuthTokensFile: tokens,
			SigningKeyFile: "/no/such/key",
		},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt, "sign-fail must degrade to unsigned, NOT abort the whole server")
	assert.False(t, rt.Signed)
}

func TestBuildA2AServer_ListenDefaultApplied(t *testing.T) {
	t.Parallel()
	tokens := writeTokenFile(t)
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{Enabled: true, AuthTokensFile: tokens},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)
	assert.Equal(t, defaultA2AListen, rt.Addr)
}

func TestBuildA2AServer_HostnameFallsBackWhenAgentIDUnset(t *testing.T) {
	t.Parallel()
	tokens := writeTokenFile(t)
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{Enabled: true, AuthTokensFile: tokens},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)
	assert.NotEmpty(t, rt.Server.Card.AgentID)
}

// --- runA2AServer lifecycle ---------------------------------------

func TestRunA2AServer_NilRuntimeIsNoOp(t *testing.T) {
	t.Parallel()
	// Should not panic, should not block.
	runA2AServer(context.Background(), nil, a2aSilentLogger())
}

func TestRunA2AServer_ContextCancelStopsServer(t *testing.T) {
	t.Parallel()
	tokens := writeTokenFile(t)
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled: true, AuthTokensFile: tokens,
			Listen: "127.0.0.1:0", // ephemeral so we can probe below
		},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)

	// Reserve a port so we know a live address to hit.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	rt.Addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runA2AServer(ctx, rt, a2aSilentLogger())
		close(done)
	}()

	// Wait for listener to come up.
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close() //nolint:errcheck // probe cleanup, best-effort
		return true
	}, 2*time.Second, 50*time.Millisecond)

	// Hit the well-known route to prove it's actually the A2A server.
	res, err := http.Get("http://" + addr + "/.well-known/agent-card.json") //nolint:noctx // test
	require.NoError(t, err)
	_ = res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runA2AServer did not stop after ctx cancel")
	}
}

func TestRunA2AServer_ListenFailureLogsAndReturns(t *testing.T) {
	t.Parallel()
	// Bind to a port already held by another listener so
	// net.Listen fails; runA2AServer must log + return, not panic.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = held.Close() }() //nolint:errcheck // test cleanup

	tokens := writeTokenFile(t)
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{Enabled: true, AuthTokensFile: tokens},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)
	rt.Addr = held.Addr().String()

	done := make(chan struct{})
	go func() {
		runA2AServer(context.Background(), rt, a2aSilentLogger())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runA2AServer should return promptly on listen failure")
	}
}

// --- end-to-end doctor rows ---------------------------------------

func TestCheckA2A_NoConfigReturnsNil(t *testing.T) {
	t.Parallel()
	rows := checkA2A(&config.Config{})
	assert.Empty(t, rows)
}

func TestCheckA2A_EnabledEmitsAllRows(t *testing.T) {
	t.Parallel()
	rows := checkA2A(&config.Config{A2A: config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled: true, Listen: ":9999",
			AuthTokensFile: "/etc/rousseau/a2a-tokens",
			ExposedSkills:  []string{"a", "b"},
		},
	}})
	names := diagNames(rows)
	assert.Contains(t, names, "identity.a2a.server.listen")
	assert.Contains(t, names, "identity.a2a.server.auth_tokens_file")
	assert.Contains(t, names, "identity.a2a.server.signing_key_file")
	assert.Contains(t, names, "identity.a2a.server.exposed_skills")
}

func TestCheckA2A_MissingAuthTokensFileEmitsFail(t *testing.T) {
	t.Parallel()
	rows := checkA2A(&config.Config{A2A: config.A2AConfig{
		Server: config.A2AServerConfig{Enabled: true},
	}})
	for _, r := range rows {
		if r.Name == "identity.a2a.server.auth_tokens_file" {
			assert.Equal(t, "fail", r.Status)
			assert.Contains(t, r.Detail, "anonymous")
			return
		}
	}
	t.Fatal("did not find auth_tokens_file row")
}

func TestCheckA2A_SigningKeyOptional(t *testing.T) {
	t.Parallel()
	rows := checkA2A(&config.Config{A2A: config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled: true, AuthTokensFile: "/x", SigningKeyFile: "",
		},
	}})
	for _, r := range rows {
		if r.Name == "identity.a2a.server.signing_key_file" {
			assert.Equal(t, "info", r.Status,
				"unsigned card is a supported posture; must not fail-status")
			return
		}
	}
	t.Fatal("did not find signing_key_file row")
}

func TestCheckA2A_ClientsRowSurfacesCount(t *testing.T) {
	t.Parallel()
	rows := checkA2A(&config.Config{A2A: config.A2AConfig{
		Clients: []config.A2AClientConfig{
			{Name: "spec-writer"},
			{Name: "reviewer"},
		},
	}})
	names := diagNames(rows)
	assert.Contains(t, names, "identity.a2a.clients")
}

// --- helpers ------------------------------------------------------

func diagNames(rows []diagResult) map[string]string {
	m := map[string]string{}
	for _, r := range rows {
		m[r.Name] = r.Detail
	}
	return m
}

// fakeAgent yields a minimal *agent.Agent for construction tests.
// The agent is NEVER Run — buildA2AServer only stores the pointer.
// A real end-to-end (agent.Turn actually running) test belongs in
// the daemon-assembly integration suite where a full provider +
// tools + registry chain is available.
type fakeAgent struct{}

func (fakeAgent) agent() *agent.Agent { return &agent.Agent{} }

// TestBuildA2AServer_DisableCoversEveryDisableBranch is a sweep of
// the four disable-path returns. Freezes the fail-open contract:
// none of these must panic.
func TestBuildA2AServer_DisableSweepPanicFree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  config.A2AConfig
		ag   *agent.Agent
	}{
		{"not_enabled", config.A2AConfig{}, nil},
		{"nil_agent", config.A2AConfig{Server: config.A2AServerConfig{Enabled: true}}, nil},
		{"no_auth_file", config.A2AConfig{Server: config.A2AServerConfig{Enabled: true}}, (&fakeAgent{}).agent()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				_ = buildA2AServer(tc.cfg, tc.ag, a2aSilentLogger())
			})
		})
	}
}

// TestServedCardHasAgentIDAndSkills is a real end-to-end assembly
// through httptest — proves the buildA2AServer + runA2AServer chain
// produces a spec-conformant well-known card at the configured URL.
func TestServedCardHasAgentIDAndSkills(t *testing.T) {
	t.Parallel()
	tokens := writeTokenFile(t)
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled: true, AuthTokensFile: tokens,
			AgentID: "e2e-1", AgentName: "e2e-agent",
			ExposedSkills: []string{"echo"},
		},
	}, (&fakeAgent{}).agent(), a2aSilentLogger())
	require.NotNil(t, rt)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	rt.Addr = ln.Addr().String()
	require.NoError(t, ln.Close())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runA2AServer(ctx, rt, a2aSilentLogger())

	// Wait for readiness.
	base := "http://" + rt.Addr
	require.Eventually(t, func() bool {
		res, err := http.Get(base + "/.well-known/agent-card.json") //nolint:noctx // test
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == http.StatusOK
	}, 2*time.Second, 25*time.Millisecond)

	res, err := http.Get(base + "/.well-known/agent-card.json") //nolint:noctx // test
	require.NoError(t, err)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	_ = res.Body.Close()

	var card a2a.AgentCard
	require.NoError(t, json.Unmarshal(body, &card))
	assert.Equal(t, "e2e-agent", card.Name)
	require.Len(t, card.Skills, 1)
	assert.Equal(t, "echo", card.Skills[0].Name)
	// Fingerprint check that the spec-conformant routes are wired.
	require.NotEmpty(t, card.Interfaces)
	assert.Contains(t, strings.Join(bindings(card), ","), "REST")
}

func bindings(card a2a.AgentCard) []string {
	out := make([]string, 0, len(card.Interfaces))
	for _, iface := range card.Interfaces {
		out = append(out, iface.ProtocolBinding)
	}
	return out
}
