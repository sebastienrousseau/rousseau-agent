package vertex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// isolateCloudEnv points every ambient Google-credential lookup at a
// throwaway HOME so a developer's real gcloud login (or a GCE metadata
// server) can never leak into these tests. GCE_METADATA_HOST is set to
// a sentinel value, which short-circuits cloud.google.com/go/compute's
// OnGCE probe so it never dials the link-local metadata address.
func isolateCloudEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CLOUDSDK_CONFIG", filepath.Join(dir, "gcloud"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GCE_METADATA_HOST", "169.254.169.254.invalid")
	t.Setenv("NO_GCE_CHECK", "true")
	return dir
}

// authorizedUserJSON is a syntactically valid ADC "authorized_user"
// document. Parsing it requires no network — the refresh token is only
// exchanged when a request actually needs a bearer token, which these
// tests never trigger.
const authorizedUserJSON = `{
  "type": "authorized_user",
  "client_id": "test-client-id.apps.googleusercontent.com",
  "client_secret": "test-client-secret",
  "refresh_token": "1//test-refresh-token"
}`

// TestNew_ApplicationDefaultCredentials covers the ADC branch of New:
// no explicit HTTPClient and no CredentialsFile, so credentials are
// discovered from GOOGLE_APPLICATION_CREDENTIALS and wrapped in an
// oauth2 transport.
func TestNew_ApplicationDefaultCredentials(t *testing.T) {
	dir := isolateCloudEnv(t)
	adc := filepath.Join(dir, "adc.json")
	require.NoError(t, os.WriteFile(adc, []byte(authorizedUserJSON), 0o600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adc)

	p, err := New(context.Background(), Config{
		Project: "proj", Region: "europe-west1", Model: "claude-sonnet-4-6@20260101",
	})
	require.NoError(t, err)
	require.NotNil(t, p.http)
	assert.NotSame(t, http.DefaultClient, p.http,
		"ADC path must install its own oauth2-backed client")
	assert.Equal(t,
		"https://europe-west1-aiplatform.googleapis.com/v1/projects/proj/"+
			"locations/europe-west1/publishers/anthropic/models/claude-sonnet-4-6@20260101:rawPredict",
		p.url)
	assert.Equal(t, int64(4096), p.cfg.MaxTokens)
}

// TestNew_CredentialsFileIsUsed covers the explicit-file branch: the
// file contents, not ADC, decide the credentials.
func TestNew_CredentialsFileIsUsed(t *testing.T) {
	isolateCloudEnv(t)
	dir := t.TempDir()
	credFile := filepath.Join(dir, "sa.json")
	require.NoError(t, os.WriteFile(credFile, []byte(authorizedUserJSON), 0o600))

	p, err := New(context.Background(), Config{
		Project: "proj", Region: "us-central1", Model: "m",
		CredentialsFile: credFile,
	})
	require.NoError(t, err)
	assert.NotNil(t, p.http)
}

// -- transport doubles -------------------------------------------------

// bodyErrTransport returns a 200 whose body fails mid-read, exercising
// Complete's io.ReadAll error branch.
type bodyErrTransport struct{ err error }

func (b *bodyErrTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(&failingReader{err: b.err}),
		Request:    req,
	}, nil
}

type failingReader struct {
	err  error
	sent bool
}

func (f *failingReader) Read(p []byte) (int, error) {
	if !f.sent {
		f.sent = true
		copy(p, "{")
		return 1, nil
	}
	return 0, f.err
}

func TestComplete_ResponseBodyReadError(t *testing.T) {
	boom := errors.New("connection reset by peer")
	p := &Provider{
		http: &http.Client{Transport: &bodyErrTransport{err: boom}},
		cfg:  Config{MaxTokens: 64},
		url:  "https://us-central1-aiplatform.googleapis.com/v1/x:rawPredict",
	}
	_, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vertex: read")
	assert.Contains(t, err.Error(), boom.Error())
}

// TestComplete_BodyBuildErrorShortCircuits proves an unconvertible
// message never reaches the transport.
func TestComplete_BodyBuildErrorShortCircuits(t *testing.T) {
	p := &Provider{
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("transport must not be reached")
			return nil, nil
		})},
		cfg: Config{MaxTokens: 64},
		url: "https://example.invalid/x:rawPredict",
	}
	_, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{{
			Role:    agent.RoleUser,
			Content: []agent.Content{{Kind: agent.ContentKind("hologram")}},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported content kind")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestComplete_InvalidURLFailsRequestBuild covers the
// http.NewRequestWithContext error branch (a control character in the
// URL is rejected before any dial happens).
func TestComplete_InvalidURLFailsRequestBuild(t *testing.T) {
	p := &Provider{
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("transport must not be reached")
			return nil, nil
		})},
		cfg: Config{MaxTokens: 64},
		url: "https://example.invalid/\x7f:rawPredict",
	}
	_, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vertex: build request")
}

// TestBuildVertexBody_DropsSystemRoleMessages proves system turns in
// the history are hoisted out of the messages array (Vertex takes the
// system prompt as a top-level field, not a message).
func TestBuildVertexBody_DropsSystemRoleMessages(t *testing.T) {
	raw, err := buildVertexBody(agent.Request{
		System: "top-level system",
		Messages: []agent.Message{
			{Role: agent.RoleSystem, Content: []agent.Content{
				{Kind: agent.ContentText, Text: "inline system that must be dropped"},
			}},
			agent.NewUserText("hello"),
		},
	}, 128)
	require.NoError(t, err)

	var body vertexRequest
	require.NoError(t, json.Unmarshal(raw, &body))
	require.Len(t, body.Messages, 1)
	assert.Equal(t, "user", body.Messages[0].Role)
	assert.Equal(t, "top-level system", body.System)
	assert.NotContains(t, string(raw), "must be dropped")
}
