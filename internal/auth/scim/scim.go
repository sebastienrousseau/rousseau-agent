// Package scim implements a minimal-but-correct SCIM 2.0
// Service Provider — the pull-based counterpart to the SSO
// `/login` bootstrap shipped in #122. Fills the
// [sso.Directory.ResolveTransportID] deferral from #114 by
// letting an IdP (Okta, Entra ID, OneLogin, Auth0, JumpCloud,
// Google Workspace) push users + groups to the daemon on their
// existing SCIM 2.0 provisioning schedule.
//
// # What ships
//
//   - HTTP handlers for /scim/v2/Users and /scim/v2/Groups
//     covering the four verbs every IdP integration expects:
//     GET (list + single), POST (create), PUT (replace),
//     DELETE.
//   - Bearer-token auth via the Authorization header.
//   - The ONE filter operator every IdP actually uses:
//     ?filter=userName eq "..." on Users and displayName eq
//     on Groups.
//   - Standard SCIM 2.0 error envelopes on failure.
//   - A [Store] interface for the SQLite-backed directory that
//     satisfies [sso.Directory.ResolveTransportID] once
//     populated.
//
// # What's deliberately not here
//
//   - PATCH (SCIM 2.0 partial updates) — IdPs typically fall
//     back to PUT full-replace when PATCH is absent, which
//     works fine for provisioning + membership sync.
//   - Complex filter expressions (`and`/`or`/co/sw/ew). The
//     one-operator filter is enough for provisioning; RBAC /
//     OPA policies do the interesting matching downstream.
//   - Bulk operations — same reason. Standard SCIM connectors
//     serialise into single-record calls when Bulk is absent.
//   - The Schema / ServiceProviderConfig / ResourceTypes
//     discovery endpoints — most IdP connectors hard-code
//     Users + Groups paths.
//
// Follow-up PRs can add any of the above without breaking the
// wire format — SCIM is versioned by URL path (`/scim/v2/`)
// and additive extensions are the norm.
//
// # Trust model
//
// One bearer token per daemon (rotate via env / secret manager).
// The token is compared with subtle.ConstantTimeCompare so
// timing-attack length probing is guarded. TLS termination is
// the operator's problem — typical deploy is behind an ingress
// or a reverse-proxy that carries the cert. The daemon serves
// plain HTTP.
package scim

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Schema URNs used in the wire format.
const (
	// UserSchema is the SCIM 2.0 User schema URN.
	UserSchema = "urn:ietf:params:scim:schemas:core:2.0:User"
	// GroupSchema is the SCIM 2.0 Group schema URN.
	GroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
	// ErrorSchema is the SCIM 2.0 error response schema URN.
	ErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	// ListResponseSchema is the SCIM 2.0 list-response schema URN.
	ListResponseSchema = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
)

// User is the SCIM 2.0 User resource shape. Fields intentionally
// mirror the SCIM spec's JSON names so `json:` tags produce the
// wire format directly.
type User struct {
	Schemas  []string `json:"schemas"`
	ID       string   `json:"id,omitempty"`
	UserName string   `json:"userName"`
	Emails   []Email  `json:"emails,omitempty"`
	Name     *Name    `json:"name,omitempty"`
	Groups   []Ref    `json:"groups,omitempty"`
	Active   bool     `json:"active"`
	Meta     Meta     `json:"meta,omitempty"`
	// ExternalID lets IdPs carry their internal identifier
	// through the round-trip — useful for correlation with
	// their own user database.
	ExternalID string `json:"externalId,omitempty"`
}

// Email is a SCIM 2.0 email entry (multi-valued attribute).
type Email struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// Name is the SCIM 2.0 complex Name attribute.
type Name struct {
	Formatted  string `json:"formatted,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
	GivenName  string `json:"givenName,omitempty"`
}

// Ref is the SCIM 2.0 reference type — used for a User's
// group memberships and a Group's members.
type Ref struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Ref     string `json:"$ref,omitempty"`
	Type    string `json:"type,omitempty"`
}

// Meta is the SCIM 2.0 resource metadata block.
type Meta struct {
	ResourceType string    `json:"resourceType,omitempty"`
	Created      time.Time `json:"created,omitempty"`
	LastModified time.Time `json:"lastModified,omitempty"`
	Location     string    `json:"location,omitempty"`
}

// Group is the SCIM 2.0 Group resource shape.
type Group struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id,omitempty"`
	DisplayName string   `json:"displayName"`
	Members     []Ref    `json:"members,omitempty"`
	Meta        Meta     `json:"meta,omitempty"`
	ExternalID  string   `json:"externalId,omitempty"`
}

// ListResponse wraps a page of resources per SCIM 2.0.
type ListResponse struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	StartIndex   int      `json:"startIndex,omitempty"`
	ItemsPerPage int      `json:"itemsPerPage,omitempty"`
	Resources    []any    `json:"Resources"`
}

// ErrorResponse is the SCIM 2.0 error envelope.
type ErrorResponse struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	SCIMType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// Sentinel errors surfaced by [Store] implementations. The
// HTTP handlers translate these into the appropriate SCIM
// status codes.
var (
	// ErrNotFound → 404. The requested resource ID doesn't
	// exist.
	ErrNotFound = errors.New("scim: resource not found")
	// ErrConflict → 409. The resource can't be created
	// because a unique constraint already holds (username /
	// displayName / externalId collision).
	ErrConflict = errors.New("scim: resource conflict")
)

// Store is the durable directory that the SCIM handlers read
// from and write to, and that the SSO integration path reads
// from to satisfy [sso.Directory.ResolveTransportID].
//
// Implementations MUST be safe for concurrent use — the HTTP
// server dispatches each request on its own goroutine.
type Store interface {
	// -- Users --

	// CreateUser assigns an ID and persists u. Returns
	// ErrConflict when the userName is already taken.
	CreateUser(ctx context.Context, u User) (User, error)
	// GetUser returns the user by ID, or ErrNotFound.
	GetUser(ctx context.Context, id string) (User, error)
	// ListUsers returns matching users. filterUserName, when
	// non-empty, restricts to userName == filter. Empty
	// returns every user (paginated with startIndex + count;
	// zero uses provider defaults).
	ListUsers(ctx context.Context, filterUserName string, startIndex, count int) ([]User, int, error)
	// ReplaceUser overwrites the user at ID with u. Returns
	// ErrNotFound when the ID is absent.
	ReplaceUser(ctx context.Context, id string, u User) (User, error)
	// DeleteUser removes the user by ID. Idempotent —
	// deleting a missing ID is not an error (matches SCIM 2.0
	// section 3.6 recommendation).
	DeleteUser(ctx context.Context, id string) error

	// -- Groups --

	CreateGroup(ctx context.Context, g Group) (Group, error)
	GetGroup(ctx context.Context, id string) (Group, error)
	ListGroups(ctx context.Context, filterDisplayName string, startIndex, count int) ([]Group, int, error)
	ReplaceGroup(ctx context.Context, id string, g Group) (Group, error)
	DeleteGroup(ctx context.Context, id string) error

	// -- Cross-cutting --

	// LookupUserByExternalID returns the user associated with
	// externalID (populated by the IdP during provisioning).
	// Enables downstream SSO code to correlate a chat
	// transport ID to an SCIM-provisioned user without going
	// through the JWT path.
	LookupUserByExternalID(ctx context.Context, externalID string) (User, error)
	// Count returns (users, groups) for doctor reporting.
	Count(ctx context.Context) (users int, groups int, err error)
}

// Server exposes SCIM endpoints over HTTP. Constructed via
// [NewServer]; started via [Server.Handler] (or
// [Server.ListenAndServe] when the daemon wants a standalone
// http.Server).
type Server struct {
	store       Store
	bearerToken string
	logger      *slog.Logger
	baseURL     string
}

// ServerConfig configures a [Server]. BearerToken is required
// — SCIM has no anonymous mode.
type ServerConfig struct {
	// Store is the durable directory backend.
	Store Store
	// BearerToken is the shared secret the IdP presents in
	// the Authorization: Bearer header. Rotated by the
	// operator via secret-manager / env var.
	BearerToken string
	// BaseURL is the daemon's externally-reachable URL used
	// for the Meta.Location field. Optional; empty uses a
	// relative /scim/v2/... path.
	BaseURL string
	// Logger receives per-request logs. Nil uses slog.Default.
	Logger *slog.Logger
}

// NewServer constructs a Server. Returns an error when Store
// or BearerToken is missing.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("scim: Store is required")
	}
	if cfg.BearerToken == "" {
		return nil, errors.New("scim: BearerToken is required (SCIM has no anonymous mode)")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		store:       cfg.Store,
		bearerToken: cfg.BearerToken,
		logger:      logger,
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
	}, nil
}

// Handler returns the http.Handler mux serving /scim/v2/*.
// Bearer-token auth is applied to every request.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/scim/v2/Users", s.handleUsers)
	mux.HandleFunc("/scim/v2/Users/", s.handleUser)
	mux.HandleFunc("/scim/v2/Groups", s.handleGroups)
	mux.HandleFunc("/scim/v2/Groups/", s.handleGroup)
	return s.authMiddleware(mux)
}

// ListenAndServe binds addr and runs the SCIM HTTP server
// until ctx is cancelled. Blocks until shutdown; suitable
// for launching in a background goroutine from the daemon
// assembly. Matches the pattern used by the A2A + metrics
// servers in the repo.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.logger.Info("scim.starting", slog.String("addr", addr))
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) //nolint:errcheck // best-effort shutdown
		<-done
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// authMiddleware enforces the Authorization: Bearer header.
// Constant-time compare against the configured token. A
// missing or wrong token returns 401 with a SCIM error
// envelope — matches what IdPs expect when their bearer
// rotates.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			writeError(w, http.StatusUnauthorized, "invalidToken", "Authorization: Bearer <token> required")
			return
		}
		got := []byte(strings.TrimPrefix(auth, prefix))
		want := []byte(s.bearerToken)
		if subtle.ConstantTimeCompare(got, want) != 1 {
			writeError(w, http.StatusUnauthorized, "invalidToken", "bearer token rejected")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// -- User handlers --

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listUsers(w, r)
	case http.MethodPost:
		s.createUser(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "", r.Method+" not allowed on /Users")
	}
}

func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/scim/v2/Users/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "invalidPath", "expected /Users/<id>")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getUser(w, r, id)
	case http.MethodPut:
		s.replaceUser(w, r, id)
	case http.MethodDelete:
		s.deleteUser(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "", r.Method+" not allowed on /Users/<id>")
	}
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	filter := parseFilter(r.URL.Query().Get("filter"), "userName")
	startIndex := parseIntWithDefault(r.URL.Query().Get("startIndex"), 1)
	count := parseIntWithDefault(r.URL.Query().Get("count"), 0)
	users, total, err := s.store.ListUsers(r.Context(), filter, startIndex, count)
	if err != nil {
		s.logAndError(w, r, err, "list users")
		return
	}
	resources := make([]any, 0, len(users))
	for _, u := range users {
		u = s.decorateUser(u)
		resources = append(resources, u)
	}
	writeJSON(w, http.StatusOK, ListResponse{
		Schemas:      []string{ListResponseSchema},
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(resources),
		Resources:    resources,
	})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "body is not valid JSON: "+err.Error())
		return
	}
	if u.UserName == "" {
		writeError(w, http.StatusBadRequest, "invalidValue", "userName is required")
		return
	}
	created, err := s.store.CreateUser(r.Context(), u)
	if err != nil {
		s.logAndError(w, r, err, "create user")
		return
	}
	writeJSON(w, http.StatusCreated, s.decorateUser(created))
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request, id string) {
	got, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		s.logAndError(w, r, err, "get user")
		return
	}
	writeJSON(w, http.StatusOK, s.decorateUser(got))
}

func (s *Server) replaceUser(w http.ResponseWriter, r *http.Request, id string) {
	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "body is not valid JSON: "+err.Error())
		return
	}
	replaced, err := s.store.ReplaceUser(r.Context(), id, u)
	if err != nil {
		s.logAndError(w, r, err, "replace user")
		return
	}
	writeJSON(w, http.StatusOK, s.decorateUser(replaced))
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		s.logAndError(w, r, err, "delete user")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -- Group handlers --

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listGroups(w, r)
	case http.MethodPost:
		s.createGroup(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "", r.Method+" not allowed on /Groups")
	}
}

func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/scim/v2/Groups/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "invalidPath", "expected /Groups/<id>")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getGroup(w, r, id)
	case http.MethodPut:
		s.replaceGroup(w, r, id)
	case http.MethodDelete:
		s.deleteGroup(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "", r.Method+" not allowed on /Groups/<id>")
	}
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	filter := parseFilter(r.URL.Query().Get("filter"), "displayName")
	startIndex := parseIntWithDefault(r.URL.Query().Get("startIndex"), 1)
	count := parseIntWithDefault(r.URL.Query().Get("count"), 0)
	groups, total, err := s.store.ListGroups(r.Context(), filter, startIndex, count)
	if err != nil {
		s.logAndError(w, r, err, "list groups")
		return
	}
	resources := make([]any, 0, len(groups))
	for _, g := range groups {
		g = s.decorateGroup(g)
		resources = append(resources, g)
	}
	writeJSON(w, http.StatusOK, ListResponse{
		Schemas:      []string{ListResponseSchema},
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(resources),
		Resources:    resources,
	})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var g Group
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "body is not valid JSON: "+err.Error())
		return
	}
	if g.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "invalidValue", "displayName is required")
		return
	}
	created, err := s.store.CreateGroup(r.Context(), g)
	if err != nil {
		s.logAndError(w, r, err, "create group")
		return
	}
	writeJSON(w, http.StatusCreated, s.decorateGroup(created))
}

func (s *Server) getGroup(w http.ResponseWriter, r *http.Request, id string) {
	got, err := s.store.GetGroup(r.Context(), id)
	if err != nil {
		s.logAndError(w, r, err, "get group")
		return
	}
	writeJSON(w, http.StatusOK, s.decorateGroup(got))
}

func (s *Server) replaceGroup(w http.ResponseWriter, r *http.Request, id string) {
	var g Group
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "body is not valid JSON: "+err.Error())
		return
	}
	replaced, err := s.store.ReplaceGroup(r.Context(), id, g)
	if err != nil {
		s.logAndError(w, r, err, "replace group")
		return
	}
	writeJSON(w, http.StatusOK, s.decorateGroup(replaced))
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.store.DeleteGroup(r.Context(), id); err != nil {
		s.logAndError(w, r, err, "delete group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -- helpers --

// decorateUser fills schemas + meta fields on the way out.
func (s *Server) decorateUser(u User) User {
	u.Schemas = []string{UserSchema}
	u.Meta.ResourceType = "User"
	if u.Meta.Created.IsZero() {
		u.Meta.Created = time.Now().UTC()
	}
	u.Meta.LastModified = time.Now().UTC()
	if s.baseURL != "" {
		u.Meta.Location = s.baseURL + "/scim/v2/Users/" + u.ID
	} else {
		u.Meta.Location = "/scim/v2/Users/" + u.ID
	}
	return u
}

func (s *Server) decorateGroup(g Group) Group {
	g.Schemas = []string{GroupSchema}
	g.Meta.ResourceType = "Group"
	if g.Meta.Created.IsZero() {
		g.Meta.Created = time.Now().UTC()
	}
	g.Meta.LastModified = time.Now().UTC()
	if s.baseURL != "" {
		g.Meta.Location = s.baseURL + "/scim/v2/Groups/" + g.ID
	} else {
		g.Meta.Location = "/scim/v2/Groups/" + g.ID
	}
	return g
}

// logAndError translates a store error into a SCIM response.
func (s *Server) logAndError(w http.ResponseWriter, r *http.Request, err error, op string) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "notFound", err.Error())
	case errors.Is(err, ErrConflict):
		writeError(w, http.StatusConflict, "uniqueness", err.Error())
	default:
		s.logger.Warn("scim.internal_error",
			slog.String("op", op),
			slog.String("path", r.URL.Path),
			slog.String("err", err.Error()),
		)
		writeError(w, http.StatusInternalServerError, "", "internal error")
	}
}

// parseFilter recognises the ONE filter shape every IdP
// actually uses: `<attribute> eq "<value>"`. Empty on
// anything more complex — the store then returns all rows
// and the IdP's connector paginates. Attribute check is
// case-insensitive.
func parseFilter(raw, attribute string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	// Split on first space to get attribute + operator + value.
	parts := strings.SplitN(trimmed, " ", 3)
	if len(parts) != 3 {
		return ""
	}
	if !strings.EqualFold(parts[0], attribute) {
		return ""
	}
	if !strings.EqualFold(parts[1], "eq") {
		return ""
	}
	// Value is quoted per SCIM spec.
	unquoted, err := unquoteSCIM(parts[2])
	if err != nil {
		return ""
	}
	return unquoted
}

// unquoteSCIM strips SCIM's double-quoted string form. Accepts
// both `"foo"` and `%22foo%22` (URL-encoded — some IdPs
// encode the quotes).
func unquoteSCIM(s string) (string, error) {
	decoded, err := url.QueryUnescape(s)
	if err == nil {
		s = decoded
	}
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("scim: filter value not quoted: %q", s)
	}
	return s[1 : len(s)-1], nil
}

// parseIntWithDefault returns the parsed integer or fallback
// when raw is empty or invalid.
func parseIntWithDefault(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) //nolint:errcheck // best-effort; client-side write failures don't have a recovery path
}

func writeError(w http.ResponseWriter, status int, scimType, detail string) {
	writeJSON(w, status, ErrorResponse{
		Schemas:  []string{ErrorSchema},
		Status:   strconv.Itoa(status),
		SCIMType: scimType,
		Detail:   detail,
	})
}
