package oauth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	sqlitestate "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// -- shared fixtures ---------------------------------------------------

// brokenEntropy stands in for a machine whose CSPRNG is unavailable —
// the failure mode every rand.Reader call site has to handle.
type brokenEntropy struct{}

func (brokenEntropy) Read([]byte) (int, error) { return 0, errors.New("entropy source unavailable") }

// withBrokenEntropy swaps crypto/rand.Reader for the duration of the
// test. Tests using it must not run in parallel.
func withBrokenEntropy(t *testing.T) {
	t.Helper()
	orig := rand.Reader
	rand.Reader = brokenEntropy{}
	t.Cleanup(func() { rand.Reader = orig })
}

// errStore is a TokenStore whose every operation can be made to fail,
// modelling a database that goes away mid-flow. putOK lets the first N
// writes through so a test can seed a row before the failure.
type errStore struct {
	rows      map[string][]byte
	putOK     int
	puts      int
	putErr    error
	getErr    error
	deleteErr error
	listErr   error
	iterErr   error
}

func newErrStore() *errStore { return &errStore{rows: map[string][]byte{}} }

func (e *errStore) rowKey(p, a string) string { return p + "|" + a }

func (e *errStore) Put(_ context.Context, provider, accountID string, ct []byte) error {
	e.puts++
	if e.putErr != nil && e.puts > e.putOK {
		return e.putErr
	}
	e.rows[e.rowKey(provider, accountID)] = ct
	return nil
}

func (e *errStore) Get(_ context.Context, provider, accountID string) ([]byte, bool, error) {
	if e.getErr != nil {
		return nil, false, e.getErr
	}
	ct, ok := e.rows[e.rowKey(provider, accountID)]
	return ct, ok, nil
}

func (e *errStore) Delete(_ context.Context, provider, accountID string) error {
	if e.deleteErr != nil {
		return e.deleteErr
	}
	delete(e.rows, e.rowKey(provider, accountID))
	return nil
}

func (e *errStore) List(context.Context) ([]StoredRow, error) {
	if e.listErr != nil {
		return nil, e.listErr
	}
	out := make([]StoredRow, 0, len(e.rows))
	for k := range e.rows {
		for i := 0; i < len(k); i++ {
			if k[i] == '|' {
				out = append(out, StoredRow{Provider: k[:i], AccountID: k[i+1:]})
				break
			}
		}
	}
	return out, nil
}

func (e *errStore) Iterate(_ context.Context, fn func(provider, accountID string, ct []byte) error) error {
	if e.iterErr != nil {
		return e.iterErr
	}
	for k, ct := range e.rows {
		for i := 0; i < len(k); i++ {
			if k[i] == '|' {
				if err := fn(k[:i], k[i+1:], ct); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// vaultOn wires a Vault around an arbitrary store with a fresh cipher.
func vaultOn(t *testing.T, store TokenStore) (*Vault, *Cipher) {
	t.Helper()
	k, err := GenerateKey()
	require.NoError(t, err)
	c, err := NewCipher(k)
	require.NoError(t, err)
	return NewVault(store, c), c
}

// -- crypto ------------------------------------------------------------

func TestGenerateKey_EntropyFailure(t *testing.T) {
	withBrokenEntropy(t)
	_, err := GenerateKey()
	require.Error(t, err)
	assert.ErrorContains(t, err, "generate key")
}

func TestCipher_Seal_EntropyFailure(t *testing.T) {
	key, err := GenerateKey()
	require.NoError(t, err)
	cipher, err := NewCipher(key)
	require.NoError(t, err)

	withBrokenEntropy(t)
	_, err = cipher.Seal([]byte("secret"), aad("google", "alice"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "read nonce")
}

// -- broker ------------------------------------------------------------

func TestBroker_Start_EntropyFailure(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{name: "google", authURL: "http://x"})

	withBrokenEntropy(t)
	_, _, err := b.Start("google")
	require.Error(t, err)
	assert.ErrorContains(t, err, "random state")
}

func TestBroker_Complete_ExchangeFailurePersistsNothing(t *testing.T) {
	store := newErrStore()
	v, _ := vaultOn(t, store)
	b := NewBroker(v)
	b.Register(&fakeProvider{
		name:    "google",
		authURL: "http://x",
		exchange: func(context.Context, string) (*Token, error) {
			return nil, errors.New("provider rejected the code")
		},
	})
	_, state, err := b.Start("google")
	require.NoError(t, err)

	_, err = b.Complete(context.Background(), state, "code", "alice")
	require.Error(t, err)
	assert.ErrorContains(t, err, "provider rejected the code")
	assert.Empty(t, store.rows, "a failed exchange must not write a row")
}

func TestBroker_Complete_StoreWriteFailure(t *testing.T) {
	store := newErrStore()
	store.putErr = errors.New("disk full")
	v, _ := vaultOn(t, store)
	b := NewBroker(v)
	b.Register(&fakeProvider{
		name:    "google",
		authURL: "http://x",
		exchange: func(context.Context, string) (*Token, error) {
			return &Token{AccessToken: "at"}, nil
		},
	})
	_, state, err := b.Start("google")
	require.NoError(t, err)

	_, err = b.Complete(context.Background(), state, "code", "alice")
	require.Error(t, err)
	assert.ErrorContains(t, err, "disk full")
}

func TestBroker_Load_StoreReadFailure(t *testing.T) {
	store := newErrStore()
	store.getErr = errors.New("db offline")
	v, _ := vaultOn(t, store)
	b := NewBroker(v)
	b.Register(&fakeProvider{name: "google"})

	_, err := b.Load(context.Background(), "google", "alice")
	require.Error(t, err)
	assert.ErrorContains(t, err, "db offline")
}

// TestBroker_Load_RefreshedTokenFailsToPersist covers the branch where
// the refresh succeeds but writing the new token back does not — the
// caller must see the error rather than a silently unpersisted token.
func TestBroker_Load_RefreshedTokenFailsToPersist(t *testing.T) {
	store := newErrStore()
	store.putOK = 1 // let the seed through, fail the write-back
	store.putErr = errors.New("disk full")
	v, _ := vaultOn(t, store)
	b := NewBroker(v)
	b.Register(&fakeProvider{
		name: "google",
		refresh: func(context.Context, string) (*Token, error) {
			return &Token{AccessToken: "at-fresh", Expiry: time.Now().Add(time.Hour)}, nil
		},
	})
	require.NoError(t, v.Put(context.Background(), "google", "alice", &Token{
		AccessToken: "at-old", RefreshToken: "rt", Expiry: time.Now().Add(-time.Minute),
	}))

	_, err := b.Load(context.Background(), "google", "alice")
	require.Error(t, err)
	assert.ErrorContains(t, err, "disk full")
}

// -- vault -------------------------------------------------------------

func TestVault_Put_UnmarshalableTokenExtra(t *testing.T) {
	v, _ := newVault(t)
	tok := &Token{AccessToken: "at", Extra: map[string]any{"ch": make(chan int)}}
	err := v.Put(context.Background(), "google", "alice", tok)
	require.Error(t, err)
	assert.ErrorContains(t, err, "marshal token")
}

func TestVault_Put_SealFailure(t *testing.T) {
	v, _ := newVault(t)
	withBrokenEntropy(t)
	err := v.Put(context.Background(), "google", "alice", &Token{AccessToken: "at"})
	require.Error(t, err)
	assert.ErrorContains(t, err, "read nonce")
}

// TestVault_Get_CiphertextIsNotJSON covers a row that decrypts cleanly
// but whose plaintext is not a Token — e.g. written by an older or
// corrupted encoder.
func TestVault_Get_CiphertextIsNotJSON(t *testing.T) {
	store := newMemStore()
	v, cipher := vaultOn(t, store)
	sealed, err := cipher.Seal([]byte("this is not json"), aad("google", "alice"))
	require.NoError(t, err)
	store.rows[key("google", "alice")] = sealed

	_, ok, err := v.Get(context.Background(), "google", "alice")
	assert.True(t, ok, "the row exists even though it cannot be decoded")
	require.Error(t, err)
	assert.ErrorContains(t, err, "unmarshal token")
}

func TestVault_List_EnumeratesRows(t *testing.T) {
	v, _ := vaultOn(t, newErrStore())
	require.NoError(t, v.Put(context.Background(), "google", "alice", &Token{AccessToken: "a"}))
	require.NoError(t, v.Put(context.Background(), "github", "bob", &Token{AccessToken: "b"}))

	rows, err := v.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestVault_List_PropagatesStoreError(t *testing.T) {
	store := newErrStore()
	store.listErr = errors.New("db offline")
	v, _ := vaultOn(t, store)
	_, err := v.List(context.Background())
	assert.ErrorContains(t, err, "db offline")
}

func TestVault_RotateKey_StopsOnUndecryptableRow(t *testing.T) {
	store := newMemStore()
	v, _ := vaultOn(t, store)
	require.NoError(t, v.Put(context.Background(), "google", "alice", &Token{AccessToken: "at"}))
	ct := store.rows[key("google", "alice")]
	ct[len(ct)-1] ^= 0x01 // tamper

	newKey, err := GenerateKey()
	require.NoError(t, err)
	newCipher, err := NewCipher(newKey)
	require.NoError(t, err)

	err = v.RotateKey(context.Background(), newCipher)
	require.Error(t, err)
	assert.ErrorContains(t, err, "rotate: open google/alice")
}

func TestVault_RotateKey_ResealFailure(t *testing.T) {
	v, _ := vaultOn(t, newMemStore())
	require.NoError(t, v.Put(context.Background(), "google", "alice", &Token{AccessToken: "at"}))
	newKey, err := GenerateKey()
	require.NoError(t, err)
	newCipher, err := NewCipher(newKey)
	require.NoError(t, err)

	withBrokenEntropy(t) // re-sealing needs a fresh nonce
	err = v.RotateKey(context.Background(), newCipher)
	require.Error(t, err)
	assert.ErrorContains(t, err, "read nonce")
}

// -- master key --------------------------------------------------------

func TestResolveMasterKey_UnresolvableHomeDirectory(t *testing.T) {
	t.Setenv("ROUSSEAU_TOKEN_KEY", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")

	_, err := ResolveMasterKey(true)
	require.Error(t, err)
	assert.ErrorContains(t, err, "home dir")
}

// TestResolveMasterKey_FallsBackToHomeStateDir covers the
// XDG_STATE_HOME-unset branch of keyFilePath.
func TestResolveMasterKey_FallsBackToHomeStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ROUSSEAU_TOKEN_KEY", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)

	k, err := ResolveMasterKey(true)
	require.NoError(t, err)
	assert.Len(t, k, KeySize)

	info, err := os.Stat(filepath.Join(home, ".local", "state", "rousseau", "token.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestResolveMasterKey_StatFailureOnUnreadablePath(t *testing.T) {
	// A regular file where a directory is expected makes Stat fail with
	// ENOTDIR — distinct from "not exists" and therefore an error.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	t.Setenv("ROUSSEAU_TOKEN_KEY", "")
	t.Setenv("XDG_STATE_HOME", file)

	_, err := ResolveMasterKey(true)
	require.Error(t, err)
	assert.ErrorContains(t, err, "stat key file")
}

func TestResolveMasterKey_GenerateEntropyFailure(t *testing.T) {
	t.Setenv("ROUSSEAU_TOKEN_KEY", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	withBrokenEntropy(t)
	_, err := ResolveMasterKey(true)
	require.Error(t, err)
	assert.ErrorContains(t, err, "generate key")
}

func TestResolveMasterKey_CannotCreateKeyDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))       // read-only: MkdirAll will fail
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:errcheck // test setup/teardown
	t.Setenv("ROUSSEAU_TOKEN_KEY", "")
	t.Setenv("XDG_STATE_HOME", dir)

	_, err := ResolveMasterKey(true)
	require.Error(t, err)
	assert.ErrorContains(t, err, "create key dir")
}

func TestResolveMasterKey_CannotWriteKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyDir := filepath.Join(dir, "rousseau")
	require.NoError(t, os.MkdirAll(keyDir, 0o700))
	require.NoError(t, os.Chmod(keyDir, 0o500))       // exists but not writable
	t.Cleanup(func() { _ = os.Chmod(keyDir, 0o700) }) //nolint:errcheck // test setup/teardown
	t.Setenv("ROUSSEAU_TOKEN_KEY", "")
	t.Setenv("XDG_STATE_HOME", dir)

	_, err := ResolveMasterKey(true)
	require.Error(t, err)
	assert.ErrorContains(t, err, "write key file")
}

// -- oauth2 provider ---------------------------------------------------

func TestOAuth2Provider_RefreshFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	p := NewOAuth2Provider("google", &oauth2.Config{
		ClientID:     "id",
		ClientSecret: "secret",
		Endpoint:     oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token"},
	})
	_, err := p.Refresh(context.Background(), "stale-refresh-token")
	require.Error(t, err)
	assert.ErrorContains(t, err, "refresh (google)")
}

// -- sqlite adapter ----------------------------------------------------

func TestSQLiteStore_ListPropagatesError(t *testing.T) {
	ctx := context.Background()
	s, err := sqlitestate.Open(ctx, ":memory:")
	require.NoError(t, err)
	inner, err := sqlitestate.NewOAuthTokens(ctx, s)
	require.NoError(t, err)
	store := NewSQLiteStore(inner)
	require.NoError(t, s.Close()) // the database is gone from here on

	_, err = store.List(ctx)
	assert.Error(t, err)
}

// -- token JSON --------------------------------------------------------

func TestToken_MarshalJSON_OmitsZeroExpiry(t *testing.T) {
	b, err := json.Marshal(Token{AccessToken: "at"})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "expiry")

	var round Token
	require.NoError(t, json.Unmarshal(b, &round))
	assert.Equal(t, "at", round.AccessToken)
	assert.True(t, round.Expiry.IsZero())
}

var _ io.Reader = brokenEntropy{}

// TestResolveMasterKey_UnreadableKeyFile covers the read failure that
// survives the mode check: a directory carrying 0600 permissions
// passes the Stat gate but cannot be read as a file.
func TestResolveMasterKey_UnreadableKeyFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "rousseau"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "rousseau", "token.key"), 0o600))
	t.Setenv("ROUSSEAU_TOKEN_KEY", "")
	t.Setenv("XDG_STATE_HOME", dir)

	_, err := ResolveMasterKey(false)
	require.Error(t, err)
	assert.ErrorContains(t, err, "read key file")
}

func TestResolveMasterKey_UnparseableKeyFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "rousseau"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rousseau", "token.key"),
		[]byte("this is not a hex key"), 0o600))
	t.Setenv("ROUSSEAU_TOKEN_KEY", "")
	t.Setenv("XDG_STATE_HOME", dir)

	_, err := ResolveMasterKey(true)
	require.Error(t, err)
	assert.ErrorContains(t, err, "parse key file")
}
