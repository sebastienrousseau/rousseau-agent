package scim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestGroups_NestedPathRejected(t *testing.T) {
	// Same as the Users variant — /Groups/<id>/<extra> is not a
	// SCIM path shape and must not be silently mistreated.
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodGet, "/scim/v2/Groups/abc/def", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// -- Users: additional gaps --

func TestUsers_MethodNotAllowedOnItem(t *testing.T) {
	// Only GET / PUT / DELETE are defined on /Users/<id> in the
	// pilot; PATCH is not yet implemented and must be rejected
	// cleanly rather than silently succeeding.
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodPatch, "/scim/v2/Users/does-not-matter", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestUsers_ReplaceMalformedJSON(t *testing.T) {
	// The Body-decode error branch of replaceUser was silent —
	// pin that PUT with junk body returns 400 invalidSyntax
	// so an IdP with a broken serialiser gets a clear message.
	ts, _ := newTestServer(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		ts.URL+"/scim/v2/Users/some-id", strings.NewReader("{not json"))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestUsers_DeleteStoreFailure(t *testing.T) {
	// deleteUser's error-branch was uncovered — surface the 500
	// so an IdP retry loop sees a real failure not a silent 204.
	srv, err := scim.NewServer(scim.ServerConfig{
		Store:       alwaysErrStore{},
		BearerToken: testToken,
		Logger:      silentLogger(),
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doJSON(t, ts, http.MethodDelete, "/scim/v2/Users/any-id", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// -- Groups: parity with Users --

func TestGroups_MethodNotAllowedOnCollection(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodPatch, "/scim/v2/Groups", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestGroups_MethodNotAllowedOnItem(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodPatch, "/scim/v2/Groups/anything", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestGroups_CreateMalformedJSON(t *testing.T) {
	ts, _ := newTestServer(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		ts.URL+"/scim/v2/Groups", strings.NewReader("{"))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestGroups_GetMissingReturnsNotFound(t *testing.T) {
	// Symmetric to the users variant — /Groups/<unknown> must
	// map to a 404 error envelope, not a bare 500.
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodGet, "/scim/v2/Groups/nope", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestGroups_ReplaceUpdatesFields(t *testing.T) {
	// replaceGroup was 0% covered — PUT /Groups/<id> is what
	// Okta uses when the operator renames a group or edits its
	// members. Pin the happy path.
	ts, _ := newTestServer(t)
	postResp := doJSON(t, ts, http.MethodPost, "/scim/v2/Groups", scim.Group{
		DisplayName: "old-name",
		Members:     []scim.Ref{{Value: "user-1"}},
	})
	defer postResp.Body.Close()
	require.Equal(t, http.StatusCreated, postResp.StatusCode)
	var created scim.Group
	require.NoError(t, json.NewDecoder(postResp.Body).Decode(&created))

	created.DisplayName = "new-name"
	created.Members = append(created.Members, scim.Ref{Value: "user-2"})
	putResp := doJSON(t, ts, http.MethodPut, "/scim/v2/Groups/"+created.ID, created)
	defer putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode)
	var updated scim.Group
	require.NoError(t, json.NewDecoder(putResp.Body).Decode(&updated))
	assert.Equal(t, "new-name", updated.DisplayName)
	assert.Len(t, updated.Members, 2)
}

func TestGroups_ReplaceMissingReturnsNotFound(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodPut, "/scim/v2/Groups/nope", scim.Group{DisplayName: "x"})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestGroups_ReplaceMalformedJSON(t *testing.T) {
	ts, _ := newTestServer(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		ts.URL+"/scim/v2/Groups/some-id", strings.NewReader("garbage"))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestGroups_DeleteMissingIsNoop(t *testing.T) {
	// Symmetric to users — SCIM §3.6 idempotent-delete guidance.
	ts, _ := newTestServer(t)
	resp := doJSON(t, ts, http.MethodDelete, "/scim/v2/Groups/gone", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestGroups_DeleteStoreFailure(t *testing.T) {
	srv, err := scim.NewServer(scim.ServerConfig{
		Store:       alwaysErrStore{},
		BearerToken: testToken,
		Logger:      silentLogger(),
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doJSON(t, ts, http.MethodDelete, "/scim/v2/Groups/any-id", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestGroups_StoreFailureBecomes500(t *testing.T) {
	// Mirrors TestUsers_StoreFailureBecomes500 for the Groups
	// collection endpoint so a broken store surfaces the same
	// way across both resource kinds.
	srv, err := scim.NewServer(scim.ServerConfig{
		Store:       alwaysErrStore{},
		BearerToken: testToken,
		Logger:      silentLogger(),
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doJSON(t, ts, http.MethodGet, "/scim/v2/Groups", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// -- decorate / meta --

func TestDecorate_UsesRelativeLocationWhenNoBaseURL(t *testing.T) {
	// The BaseURL-empty branch of decorateUser / decorateGroup
	// was previously uncovered. When operator hasn't configured
	// auth.sso.scim.base_url the Location must fall back to the
	// relative path — still spec-compliant, just missing the
	// canonical hostname.
	store := newMemStore()
	srv, err := scim.NewServer(scim.ServerConfig{
		Store:       store,
		BearerToken: testToken,
		Logger:      silentLogger(),
		// BaseURL deliberately empty.
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// User path.
	createUser := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{UserName: "alice"})
	defer createUser.Body.Close()
	var u scim.User
	require.NoError(t, json.NewDecoder(createUser.Body).Decode(&u))
	assert.Equal(t, "/scim/v2/Users/"+u.ID, u.Meta.Location, "no BaseURL → relative Location")

	// Group path.
	createGroup := doJSON(t, ts, http.MethodPost, "/scim/v2/Groups", scim.Group{DisplayName: "eng"})
	defer createGroup.Body.Close()
	var g scim.Group
	require.NoError(t, json.NewDecoder(createGroup.Body).Decode(&g))
	assert.Equal(t, "/scim/v2/Groups/"+g.ID, g.Meta.Location, "no BaseURL → relative Location")
}

// -- filter / pagination parsing --

func TestListPagination_InvalidCountFallsBackToDefault(t *testing.T) {
	// parseIntWithDefault's invalid / negative branches were the
	// least-covered code in the file. Exercised via ?count=<junk>
	// on the users list — the server must interpret as "no cap"
	// (fallback 0), not return an error.
	ts, _ := newTestServer(t)
	for _, name := range []string{"a", "b", "c"} {
		r := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{UserName: name})
		require.NoError(t, r.Body.Close())
	}
	// Junk count string → parseIntWithDefault falls back.
	resp := doJSON(t, ts, http.MethodGet, "/scim/v2/Users?count=not-a-number", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list scim.ListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	assert.Equal(t, 3, list.TotalResults)

	// Negative count string → also fallback.
	respNeg := doJSON(t, ts, http.MethodGet, "/scim/v2/Users?count=-5", nil)
	defer respNeg.Body.Close()
	require.Equal(t, http.StatusOK, respNeg.StatusCode)
}

func TestListFilter_NonMatchingAttributeReturnsAllRows(t *testing.T) {
	// parseFilter returns "" (all rows) when the filter names an
	// attribute the endpoint does not recognise. Some IdPs send
	// `active eq true` speculatively during discovery — we must
	// not error, just ignore.
	ts, _ := newTestServer(t)
	for _, name := range []string{"a", "b"} {
		r := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{UserName: name})
		require.NoError(t, r.Body.Close())
	}
	resp := doJSON(t, ts, http.MethodGet, `/scim/v2/Users?filter=active+eq+%22true%22`, nil)
	defer resp.Body.Close()
	var list scim.ListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	assert.Equal(t, 2, list.TotalResults, "unknown filter attribute → treat as no filter")
}

func TestListFilter_URLEncodedQuotesAccepted(t *testing.T) {
	// unquoteSCIM decodes %22-encoded quotes so IdPs that URL-
	// escape aggressively still work end-to-end. This test
	// specifically hits the URL-encoded path (not the bare
	// quote path already covered by ListWithFilter).
	ts, _ := newTestServer(t)
	r := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{UserName: "alice"})
	require.NoError(t, r.Body.Close())

	resp := doJSON(t, ts, http.MethodGet,
		"/scim/v2/Users?filter=userName%20eq%20%22alice%22", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list scim.ListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	assert.Equal(t, 1, list.TotalResults)
}

func TestListFilter_MalformedFilterReturnsAll(t *testing.T) {
	// Various malformed filter shapes must degrade to "no filter"
	// rather than error — the spec allows the server to ignore
	// unrecognised filters and IdPs rely on that gracefully.
	ts, _ := newTestServer(t)
	for _, name := range []string{"a", "b"} {
		r := doJSON(t, ts, http.MethodPost, "/scim/v2/Users", scim.User{UserName: name})
		require.NoError(t, r.Body.Close())
	}
	for _, filter := range []string{
		"only-one-token",               // fewer than 3 parts
		"userName+gt+%22alice%22",      // unrecognised operator
		"userName+eq+alice-not-quoted", // value not quoted
	} {
		resp := doJSON(t, ts, http.MethodGet, "/scim/v2/Users?filter="+filter, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, filter)
		var list scim.ListResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
		assert.Equal(t, 2, list.TotalResults, "filter %q should degrade to no-filter", filter)
		resp.Body.Close()
	}
}

// -- ListenAndServe lifecycle --

func TestListenAndServe_StartsAndShutsDownOnContextCancel(t *testing.T) {
	// ListenAndServe was 0% covered. Pin the lifecycle: it
	// returns nil when ctx is cancelled (graceful shutdown path)
	// so a caller wiring it into daemon-shutdown does not see a
	// spurious error. Uses port :0 so parallel test runs cannot
	// collide.
	srv, err := scim.NewServer(scim.ServerConfig{
		Store:       newMemStore(),
		BearerToken: testToken,
		Logger:      silentLogger(),
	})
	require.NoError(t, err)

	// Bind to an ephemeral port and hand back the address the
	// kernel chose. We resolve via a throwaway listener so we
	// avoid the port-race entirely.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close()) // ListenAndServe will re-bind

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx, addr) }()

	// Wait briefly for the goroutine to reach ListenAndServe
	// so cancel actually triggers the ctx.Done branch (not the
	// early-return-before-serve one).
	//
	// Poll instead of a fixed sleep so slow CI does not flake
	// and fast local runs do not waste time.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close() //nolint:errcheck // probe close is best-effort
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "context-cancel path returns nil")
	case <-time.After(6 * time.Second):
		t.Fatal("ListenAndServe did not return within shutdown grace")
	}
}
