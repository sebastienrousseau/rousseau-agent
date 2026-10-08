package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hitServer counts requests and the bearer tokens it was sent.
type hitServer struct {
	srv    *httptest.Server
	hits   atomic.Int32
	bearer atomic.Int32
}

func newHitServer(t *testing.T, h http.HandlerFunc) *hitServer {
	t.Helper()
	hs := &hitServer{}
	hs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hs.hits.Add(1)
		if r.Header.Get("Authorization") != "" {
			hs.bearer.Add(1)
		}
		if h != nil {
			h(w, r)
			return
		}
		_, _ = w.Write(pngHeader) //nolint:errcheck // fixture
	}))
	t.Cleanup(hs.srv.Close)
	return hs
}

// trustFileServer points c's file-host check at srv, standing in for
// https://files.slack.com.
func trustFileServer(t *testing.T, c *Client, srv *httptest.Server) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c.files.scheme, c.files.host = u.Scheme, u.Host
}

func TestDownloadFile_OtherHostRefusedWithoutRequest(t *testing.T) {
	hs := newHitServer(t, nil)
	c := newTestClient(t, Config{})
	_, err := c.downloadFile(context.Background(), hs.srv.URL+"/files/F1")
	require.Error(t, err)
	assert.Equal(t, int32(0), hs.hits.Load(), "no request may reach a host other than files.slack.com")
}

func TestDispatchEvent_OtherHostFileNotFetched(t *testing.T) {
	hs := newHitServer(t, nil)
	c := newTestClient(t, Config{})
	require.NoError(t, c.dispatchEvent(context.Background(), eventsAPIPayload{Event: slackEvent{
		Type: "message", SubType: "file_share", User: "U1", Channel: "C1", Text: "look",
		Files: []slackFile{{ID: "F1", Mimetype: "image/png", URLPrivateDownload: hs.srv.URL + "/files/F1"}},
	}}, noopHandler()))
	assert.Equal(t, int32(0), hs.hits.Load())
}

func TestFileFetcher_AllowsOnlyHTTPSFilesSlackCom(t *testing.T) {
	f := newFileFetcher(http.DefaultClient)
	assert.True(t, f.allowed("https://files.slack.com/files-pri/T1-F1/download/a.png"))
	for _, raw := range []string{
		"http://files.slack.com/f",
		"https://files.slack.com.evil.example/f",
		"https://evil.example/files.slack.com",
		"https://files.slack.com:8443/f",
		"https://user@files.slack.com/f",
		"https://FILES.slack.com/f",
		"://bad",
	} {
		assert.False(t, f.allowed(raw), "%q", raw)
	}
}

func TestDownloadFile_CrossHostRedirectRefused(t *testing.T) {
	evil := newHitServer(t, nil)
	trusted := newHitServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.srv.URL+"/steal", http.StatusFound)
	})
	c := newTestClient(t, Config{})
	trustFileServer(t, c, trusted.srv)
	_, err := c.downloadFile(context.Background(), trusted.srv.URL+"/files/F1")
	require.Error(t, err)
	assert.Equal(t, int32(0), evil.hits.Load(), "the redirect target must not be requested")
}

func TestDownloadFile_SameHostRedirectKeepsToken(t *testing.T) {
	hs := newHitServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files/F1" {
			http.Redirect(w, r, "/files/F1/real", http.StatusFound)
			return
		}
		_, _ = w.Write(pngHeader) //nolint:errcheck // fixture
	})
	c := newTestClient(t, Config{})
	trustFileServer(t, c, hs.srv)
	body, err := c.downloadFile(context.Background(), hs.srv.URL+"/files/F1")
	require.NoError(t, err)
	assert.Equal(t, pngHeader, body)
	assert.Equal(t, int32(2), hs.bearer.Load())
}

func TestDownloadFile_OversizedBodyErrors(t *testing.T) {
	hs := newHitServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 9))) //nolint:errcheck // fixture
	})
	c := newTestClient(t, Config{})
	trustFileServer(t, c, hs.srv)
	c.files.limit = 8
	_, err := c.downloadFile(context.Background(), hs.srv.URL+"/files/F1")
	require.Error(t, err, "a body over the cap is an error, not a silent truncation")
}

func TestDownloadFile_BodyAtCapIsAccepted(t *testing.T) {
	hs := newHitServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 8))) //nolint:errcheck // fixture
	})
	c := newTestClient(t, Config{})
	trustFileServer(t, c, hs.srv)
	c.files.limit = 8
	body, err := c.downloadFile(context.Background(), hs.srv.URL+"/files/F1")
	require.NoError(t, err)
	assert.Len(t, body, 8)
}
