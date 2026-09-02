package scim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
)

// memStore is an in-memory scim.Store for handler tests.
// Simulates SQLite semantics: unique-constraint enforcement on
// userName / displayName / externalId, ErrNotFound on missing
// ID, idempotent delete.
type memStore struct {
	mu     sync.Mutex
	users  map[string]scim.User
	groups map[string]scim.Group
}

func newMemStore() *memStore {
	return &memStore{
		users:  map[string]scim.User{},
		groups: map[string]scim.Group{},
	}
}

func (s *memStore) CreateUser(_ context.Context, u scim.User) (scim.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.users {
		if existing.UserName == u.UserName {
			return scim.User{}, scim.ErrConflict
		}
	}
	if u.ID == "" {
		u.ID = "user-" + u.UserName
	}
	s.users[u.ID] = u
	return u, nil
}

func (s *memStore) GetUser(_ context.Context, id string) (scim.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return scim.User{}, scim.ErrNotFound
	}
	return u, nil
}

func (s *memStore) ListUsers(_ context.Context, filterUserName string, _, _ int) ([]scim.User, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []scim.User
	for _, u := range s.users {
		if filterUserName != "" && u.UserName != filterUserName {
			continue
		}
		out = append(out, u)
	}
	return out, len(out), nil
}

func (s *memStore) ReplaceUser(_ context.Context, id string, u scim.User) (scim.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[id]; !ok {
		return scim.User{}, scim.ErrNotFound
	}
	u.ID = id
	s.users[id] = u
	return u, nil
}

func (s *memStore) DeleteUser(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.users, id)
	return nil
}

func (s *memStore) CreateGroup(_ context.Context, g scim.Group) (scim.Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.groups {
		if existing.DisplayName == g.DisplayName {
			return scim.Group{}, scim.ErrConflict
		}
	}
	if g.ID == "" {
		g.ID = "group-" + g.DisplayName
	}
	s.groups[g.ID] = g
	return g, nil
}

func (s *memStore) GetGroup(_ context.Context, id string) (scim.Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return scim.Group{}, scim.ErrNotFound
	}
	return g, nil
}

func (s *memStore) ListGroups(_ context.Context, filterDisplayName string, _, _ int) ([]scim.Group, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []scim.Group
	for _, g := range s.groups {
		if filterDisplayName != "" && g.DisplayName != filterDisplayName {
			continue
		}
		out = append(out, g)
	}
	return out, len(out), nil
}

func (s *memStore) ReplaceGroup(_ context.Context, id string, g scim.Group) (scim.Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[id]; !ok {
		return scim.Group{}, scim.ErrNotFound
	}
	g.ID = id
	s.groups[id] = g
	return g, nil
}

func (s *memStore) DeleteGroup(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.groups, id)
	return nil
}

func (s *memStore) LookupUserByExternalID(_ context.Context, externalID string) (scim.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.ExternalID != "" && u.ExternalID == externalID {
			return u, nil
		}
	}
	return scim.User{}, scim.ErrNotFound
}

func (s *memStore) Count(context.Context) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users), len(s.groups), nil
}

// -- helpers --

const testToken = "test-bearer-token"

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestServer builds a Server + wraps it in an httptest.Server.
func newTestServer(t *testing.T) (*httptest.Server, *memStore) {
	t.Helper()
	store := newMemStore()
	srv, err := scim.NewServer(scim.ServerConfig{
		Store:       store,
		BearerToken: testToken,
		BaseURL:     "https://rousseau.example.com",
		Logger:      silentLogger(),
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, store
}

func doJSON(t *testing.T, ts *httptest.Server, method, path string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, ts.URL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/scim+json")
	}
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	return resp
}

// -- construction --

func TestNewServer_RequiresStore(t *testing.T) {
	_, err := scim.NewServer(scim.ServerConfig{BearerToken: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Store is required")
}

func TestNewServer_RequiresBearerToken(t *testing.T) {
	_, err := scim.NewServer(scim.ServerConfig{Store: newMemStore()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BearerToken is required")
}

// -- auth --

func TestAuth_MissingHeaderRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/scim/v2/Users", nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_WrongTokenRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/scim/v2/Users", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// -- User CRUD --

func TestUsers_CreateReadUpdateDelete(t *testing.T) {
	ts, _ := newTestServer(t)

	// Create.
	resp := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{
		UserName: "alice",
		Emails:   []scim.Email{{Value: "alice@example.com", Primary: true}},
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created scim.User
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	require.NotEmpty(t, created.ID)
	assert.Equal(t, "alice", created.UserName)
	assert.Equal(t, "https://rousseau.example.com/scim/v2/Users/"+created.ID, created.Meta.Location)

	// Get.
	getResp := doJSON(t, ts, http.MethodGet, "/scim/v2/Users/"+created.ID, nil)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)
	var fetched scim.User
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&fetched))
	assert.Equal(t, "alice", fetched.UserName)

	// Replace (PUT).
	fetched.Name = &scim.Name{FamilyName: "Example", GivenName: "Alice"}
	putResp := doJSON(t, ts, http.MethodPut, "/scim/v2/Users/"+created.ID, fetched)
	defer putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode)

	// Delete.
	delResp := doJSON(t, ts, http.MethodDelete, "/scim/v2/Users/"+created.ID, nil)
	defer delResp.Body.Close()
	require.Equal(t, http.StatusNoContent, delResp.StatusCode)

	// Get again → 404.
	missResp := doJSON(t, ts, http.MethodGet, "/scim/v2/Users/"+created.ID, nil)
	defer missResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, missResp.StatusCode)
}

func TestUsers_CreateWithoutUserNameRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{})
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var e scim.ErrorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&e))
	assert.Equal(t, "invalidValue", e.SCIMType)
}

func TestUsers_DuplicateUserNameConflict(t *testing.T) {
	ts, _ := newTestServer(t)
	first := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{UserName: "alice"})
	defer first.Body.Close()
	require.Equal(t, http.StatusCreated, first.StatusCode)

	second := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{UserName: "alice"})
	defer second.Body.Close()
	require.Equal(t, http.StatusConflict, second.StatusCode)
	var e scim.ErrorResponse
	require.NoError(t, json.NewDecoder(second.Body).Decode(&e))
	assert.Equal(t, "uniqueness", e.SCIMType)
}

func TestUsers_ListWithFilter(t *testing.T) {
	// Load-bearing: IdPs poll `?filter=userName eq "alice"`
	// before every provisioning step. The one filter shape
	// must work end-to-end.
	ts, _ := newTestServer(t)
	for _, name := range []string{"alice", "bob", "carol"} {
		r := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{UserName: name})
		require.Equal(t, http.StatusCreated, r.StatusCode)
		r.Body.Close()
	}
	resp := doJSON(t, ts, http.MethodGet, `/scim/v2/Users?filter=userName+eq+%22alice%22`, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list scim.ListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	assert.Equal(t, 1, list.TotalResults)
	assert.Equal(t, 1, len(list.Resources))
}

func TestUsers_ListWithoutFilterReturnsAll(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, name := range []string{"a", "b", "c"} {
		r := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{UserName: name})
		require.NoError(t, r.Body.Close())
	}
	resp := doJSON(t, ts, http.MethodGet, "/scim/v2/Users", nil)
	defer resp.Body.Close()
	var list scim.ListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	assert.Equal(t, 3, list.TotalResults)
}

func TestUsers_MethodNotAllowed(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodPatch, "/scim/v2/Users", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestUsers_ReplaceMissingReturnsNotFound(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodPut, "/scim/v2/Users/does-not-exist", scim.User{UserName: "x"})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestUsers_DeleteMissingIsNoop(t *testing.T) {
	// Load-bearing: SCIM §3.6 recommends idempotent delete
	// so IdPs can retry safely.
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodDelete, "/scim/v2/Users/not-there", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestUsers_MalformedJSONRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/scim/v2/Users", strings.NewReader("{"))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// -- Group CRUD --

func TestGroups_CreateReadDelete(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodPost, "/scim/v2/Groups", scim.Group{
		DisplayName: "platform-eng",
		Members: []scim.Ref{
			{Value: "user-alice"},
			{Value: "user-bob"},
		},
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var g scim.Group
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&g))
	require.NotEmpty(t, g.ID)
	assert.Len(t, g.Members, 2)

	// Get.
	getResp := doJSON(t, ts, http.MethodGet, "/scim/v2/Groups/"+g.ID, nil)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	// Delete.
	delResp := doJSON(t, ts, http.MethodDelete, "/scim/v2/Groups/"+g.ID, nil)
	defer delResp.Body.Close()
	assert.Equal(t, http.StatusNoContent, delResp.StatusCode)
}

func TestGroups_CreateWithoutDisplayNameRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodPost, "/scim/v2/Groups", scim.Group{})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestGroups_ListWithFilter(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, name := range []string{"eng", "sre", "product"} {
		r := doJSON(t, ts, http.MethodPost, "/scim/v2/Groups", scim.Group{DisplayName: name})
		require.NoError(t, r.Body.Close())
	}
	resp := doJSON(t, ts, http.MethodGet, `/scim/v2/Groups?filter=displayName+eq+%22sre%22`, nil)
	defer resp.Body.Close()
	var list scim.ListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	assert.Equal(t, 1, list.TotalResults)
}

// -- Error paths / store failures --

// alwaysErrStore surfaces a non-sentinel error for every op —
// exercises the "internal error" branch of the handler
// translator.
type alwaysErrStore struct{}

func (alwaysErrStore) CreateUser(context.Context, scim.User) (scim.User, error) {
	return scim.User{}, errors.New("boom")
}
func (alwaysErrStore) GetUser(context.Context, string) (scim.User, error) {
	return scim.User{}, errors.New("boom")
}
func (alwaysErrStore) ListUsers(context.Context, string, int, int) ([]scim.User, int, error) {
	return nil, 0, errors.New("boom")
}
func (alwaysErrStore) ReplaceUser(context.Context, string, scim.User) (scim.User, error) {
	return scim.User{}, errors.New("boom")
}
func (alwaysErrStore) DeleteUser(context.Context, string) error { return errors.New("boom") }
func (alwaysErrStore) CreateGroup(context.Context, scim.Group) (scim.Group, error) {
	return scim.Group{}, errors.New("boom")
}
func (alwaysErrStore) GetGroup(context.Context, string) (scim.Group, error) {
	return scim.Group{}, errors.New("boom")
}
func (alwaysErrStore) ListGroups(context.Context, string, int, int) ([]scim.Group, int, error) {
	return nil, 0, errors.New("boom")
}
func (alwaysErrStore) ReplaceGroup(context.Context, string, scim.Group) (scim.Group, error) {
	return scim.Group{}, errors.New("boom")
}
func (alwaysErrStore) DeleteGroup(context.Context, string) error { return errors.New("boom") }
func (alwaysErrStore) LookupUserByExternalID(context.Context, string) (scim.User, error) {
	return scim.User{}, errors.New("boom")
}
func (alwaysErrStore) Count(context.Context) (int, int, error) { return 0, 0, errors.New("boom") }

func TestUsers_StoreFailureBecomes500(t *testing.T) {
	srv, err := scim.NewServer(scim.ServerConfig{
		Store:       alwaysErrStore{},
		BearerToken: testToken,
		Logger:      silentLogger(),
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doJSON(t, ts, http.MethodGet, "/scim/v2/Users", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// -- path parsing --

func TestUsers_NestedPathRejected(t *testing.T) {
	// /Users/abc/def is undefined in SCIM 2.0 — must be
	// rejected as a bad path rather than silently mistreated.
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodGet, "/scim/v2/Users/abc/def", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
