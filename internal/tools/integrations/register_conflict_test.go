package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// squatter is a tools.Tool that occupies a name a suite is about to
// claim, so RegisterAll's per-suite registration error is exercised.
type squatter struct{ name string }

func (s squatter) Name() string                                           { return s.name }
func (squatter) Description() string                                      { return "test squatter" }
func (squatter) InputSchema() map[string]any                              { return map[string]any{"type": "object"} }
func (squatter) Execute(context.Context, json.RawMessage) (string, error) { return "", nil }

// TestRegisterAll_SuiteRegistrationConflictAborts asserts that a name
// collision inside any suite is surfaced to the operator at startup
// rather than silently dropping a tool.
func TestRegisterAll_SuiteRegistrationConflictAborts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		squat     string
		cfg       Config
		wantInErr string
	}{
		{
			name:      "github",
			squat:     "github_list_repos",
			cfg:       Config{GitHub: GitHubConfig{Enabled: true, Token: "gh"}},
			wantInErr: "github: register github_list_repos",
		},
		{
			name:      "slack",
			squat:     "slack_post_message",
			cfg:       Config{Slack: SlackConfig{Enabled: true, BotToken: "xoxb-t"}},
			wantInErr: "slack: register slack_post_message",
		},
		{
			name:  "google",
			squat: "gmail_list",
			cfg: Config{Google: GoogleConfig{
				Enabled: true,
				TokenFn: func(context.Context) (string, error) { return "at", nil },
			}},
			wantInErr: "google: register gmail_list",
		},
		{
			name:      "linear",
			squat:     "linear_list_issues",
			cfg:       Config{Linear: LinearConfig{Enabled: true, APIKey: "lin_x"}},
			wantInErr: "linear: register linear_list_issues",
		},
		{
			name:      "stripe",
			squat:     "stripe_list_charges",
			cfg:       Config{Stripe: StripeConfig{Enabled: true, SecretKey: "sk_x"}},
			wantInErr: "stripe: register stripe_list_charges",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewRegistry()
			require.NoError(t, reg.Register(squatter{name: tc.squat}))
			err := RegisterAll(reg, tc.cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
			assert.ErrorContains(t, err, tc.wantInErr)
		})
	}
}

// newComposioStub serves Composio's /actions discovery endpoint.
func newComposioStub(t *testing.T, status int, body string) (*httptest.Server, *http.Header) {
	t.Helper()
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		assert.Equal(t, "/actions", r.URL.Path)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body)) //nolint:errcheck // test fixture
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestRegisterAll_ComposioRegistersDiscoveredActions(t *testing.T) {
	srv, header := newComposioStub(t, http.StatusOK, `{"items":[
		{"name":"GMAIL_SEND_EMAIL","appKey":"gmail","description":"Send an email","parameters":{"type":"object"}},
		{"name":"SLACK_POST","appKey":"slack","description":"Post"}
	]}`)

	var logBuf bytes.Buffer
	reg := tools.NewRegistry()
	err := RegisterAll(reg, Config{Composio: ComposioConfig{
		Enabled: true,
		APIKey:  "cx_key",
		UserID:  "user-1",
		BaseURL: srv.URL,
		Apps:    []string{"GMAIL"}, // case-insensitive filter
	}}, slog.New(slog.NewTextHandler(&logBuf, nil)))
	require.NoError(t, err)

	assert.Equal(t, []string{"cx_gmail_gmail_send_email"}, reg.Names(),
		"only actions from the allow-listed app are registered")
	assert.Equal(t, "cx_key", header.Get("x-api-key"))
	assert.Contains(t, logBuf.String(), "suite=composio")
	assert.Contains(t, logBuf.String(), "action_count=1")
}

func TestRegisterAll_ComposioDiscoveryFailureAborts(t *testing.T) {
	srv, _ := newComposioStub(t, http.StatusUnauthorized, `{"error":"invalid api key"}`)
	reg := tools.NewRegistry()
	err := RegisterAll(reg, Config{Composio: ComposioConfig{
		Enabled: true,
		APIKey:  "bad",
		UserID:  "user-1",
		BaseURL: srv.URL,
	}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrations/composio")
	assert.Contains(t, err.Error(), "401")
	assert.Empty(t, reg.Names())
}

func TestRegisterAll_NilLoggerFallsBackToDefault(t *testing.T) {
	// A nil logger must not panic — RegisterAll substitutes
	// slog.Default() before emitting its per-suite events.
	reg := tools.NewRegistry()
	require.NoError(t, RegisterAll(reg, Config{
		Stripe: StripeConfig{Enabled: true, SecretKey: "sk_x"},
	}, nil))
	assert.Len(t, reg.Names(), 2)
}
