package a2a_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// stubResolver satisfies a2a.Resolver by returning a canned response.
type stubResolver struct {
	body []byte
	ct   string
	err  error
	seen []string
}

func (s *stubResolver) Resolve(_ context.Context, uri string) ([]byte, string, error) {
	s.seen = append(s.seen, uri)
	return s.body, s.ct, s.err
}

func TestDefaultFetcher_EmptyURIErrors(t *testing.T) {
	f := &a2a.DefaultFetcher{}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{})
	assert.Error(t, err)
}

func TestDefaultFetcher_DataURI_Base64Payload(t *testing.T) {
	// data:application/json;base64,<payload>
	payload := base64.StdEncoding.EncodeToString([]byte(`{"hi":true}`))
	uri := "data:application/json;base64," + payload
	f := &a2a.DefaultFetcher{}
	data, ct, err := f.Fetch(context.Background(), a2a.Artifact{URI: uri})
	require.NoError(t, err)
	assert.Equal(t, `{"hi":true}`, string(data))
	assert.Equal(t, "application/json", ct)
}

func TestDefaultFetcher_DataURI_PercentEncodedFallsBackToOctetStream(t *testing.T) {
	// data:,hello%20world  — no mediatype, no base64 → octet-stream.
	f := &a2a.DefaultFetcher{}
	data, ct, err := f.Fetch(context.Background(), a2a.Artifact{URI: "data:,hello%20world"})
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))
	assert.Equal(t, "application/octet-stream", ct)
}

func TestDefaultFetcher_DataURI_BadBase64(t *testing.T) {
	f := &a2a.DefaultFetcher{}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "data:;base64,not@base64!"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode base64")
}

func TestDefaultFetcher_DataURI_MissingComma(t *testing.T) {
	f := &a2a.DefaultFetcher{}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "data:no-comma-here"})
	assert.Error(t, err)
}

func TestDefaultFetcher_DataURI_EnforcesSizeCap(t *testing.T) {
	// 10-byte cap; try to sneak 32 bytes past it.
	huge := strings.Repeat("a", 32)
	uri := "data:," + huge
	f := &a2a.DefaultFetcher{MaxBytes: 10}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: uri})
	assert.ErrorIs(t, err, a2a.ErrArtifactTooLarge)
}

func TestDefaultFetcher_HTTP_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		if _, err := w.Write([]byte("# hi from http")); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()

	f := &a2a.DefaultFetcher{}
	data, ct, err := f.Fetch(context.Background(), a2a.Artifact{URI: srv.URL})
	require.NoError(t, err)
	assert.Equal(t, "# hi from http", string(data))
	assert.Equal(t, "text/markdown", ct)
}

func TestDefaultFetcher_HTTP_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	defer srv.Close()

	f := &a2a.DefaultFetcher{}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: srv.URL})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 410")
}

func TestDefaultFetcher_HTTP_SizeCapEnforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Stream >cap bytes; LimitReader should catch us.
		if _, err := w.Write([]byte(strings.Repeat("x", 1024))); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()

	f := &a2a.DefaultFetcher{MaxBytes: 256}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: srv.URL})
	assert.ErrorIs(t, err, a2a.ErrArtifactTooLarge)
}

func TestDefaultFetcher_HTTP_CrossOriginRedirectRefused(t *testing.T) {
	// Two servers; the first 302s to the second. Fetch should error.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte("secret from another origin")); err != nil {
			t.Errorf("target write: %v", err)
		}
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	f := &a2a.DefaultFetcher{} // uses DefaultHTTPClient which refuses cross-origin
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: redirector.URL})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cross-origin redirect refused")
}

func TestDefaultFetcher_HTTP_SameOriginRedirectAllowed(t *testing.T) {
	// One mux, one origin. /r → /t. Should succeed.
	mux := http.NewServeMux()
	mux.HandleFunc("/t", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte("landed")); err != nil {
			t.Errorf("target write: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/r", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/t", http.StatusFound)
	})

	f := &a2a.DefaultFetcher{}
	data, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: srv.URL + "/r"})
	require.NoError(t, err)
	assert.Equal(t, "landed", string(data))
}

func TestDefaultFetcher_ArtifactScheme_DispatchesToResolver(t *testing.T) {
	res := &stubResolver{body: []byte("resolved payload"), ct: "text/plain"}
	f := &a2a.DefaultFetcher{Resolver: res}
	data, ct, err := f.Fetch(context.Background(), a2a.Artifact{URI: "artifact://peer-a/123"})
	require.NoError(t, err)
	assert.Equal(t, "resolved payload", string(data))
	assert.Equal(t, "text/plain", ct)
	assert.Equal(t, []string{"artifact://peer-a/123"}, res.seen)
}

func TestDefaultFetcher_ArtifactScheme_NoResolverIsUnsupported(t *testing.T) {
	f := &a2a.DefaultFetcher{} // no Resolver
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "artifact://peer/x"})
	assert.ErrorIs(t, err, a2a.ErrUnsupportedScheme)
}

func TestDefaultFetcher_ArtifactScheme_ResolverErrorPropagates(t *testing.T) {
	res := &stubResolver{err: errors.New("peer offline")}
	f := &a2a.DefaultFetcher{Resolver: res}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "artifact://peer/x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "peer offline")
}

func TestDefaultFetcher_ArtifactScheme_ResolverPayloadCapped(t *testing.T) {
	res := &stubResolver{body: []byte(strings.Repeat("z", 2048))}
	f := &a2a.DefaultFetcher{Resolver: res, MaxBytes: 256}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "artifact://peer/x"})
	assert.ErrorIs(t, err, a2a.ErrArtifactTooLarge)
}

func TestDefaultFetcher_UnknownScheme(t *testing.T) {
	f := &a2a.DefaultFetcher{}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "ftp://old/school"})
	assert.ErrorIs(t, err, a2a.ErrUnsupportedScheme)
	assert.Contains(t, err.Error(), "ftp")
}

func TestDefaultFetcher_NegativeMaxDisablesCap(t *testing.T) {
	// MaxBytes < 0 is a test-only sanity for large fixtures. Confirm
	// it does what it says on the tin.
	f := &a2a.DefaultFetcher{MaxBytes: -1}
	huge := strings.Repeat("a", 64*1024)
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "data:," + huge})
	require.NoError(t, err)
}

func TestDefaultFetcher_DataURI_EncodedSizePreCheckRejects(t *testing.T) {
	// The base64-encoded payload weighs ~4/3 the decoded size, so
	// the fast-path check (encoded > cap*2) fires before we spend
	// the CPU on the actual base64 decode. Payload larger than 2×cap.
	huge := strings.Repeat("a", 64) // encoded length 64 > 10*2
	f := &a2a.DefaultFetcher{MaxBytes: 10}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "data:;base64," + huge})
	require.Error(t, err)
	assert.ErrorIs(t, err, a2a.ErrArtifactTooLarge)
}

func TestDefaultFetcher_DataURI_MediaTypeAndBase64Coexist(t *testing.T) {
	// "data:text/plain;base64,SGVsbG8=" — mediatype first, then
	// ;base64. Both correctly picked up.
	f := &a2a.DefaultFetcher{}
	data, ct, err := f.Fetch(context.Background(), a2a.Artifact{URI: "data:text/plain;base64,SGVsbG8="})
	require.NoError(t, err)
	assert.Equal(t, "Hello", string(data))
	assert.Equal(t, "text/plain", ct)
}

func TestDefaultFetcher_DataURI_BadPercentEncoding(t *testing.T) {
	// data:,%zz — invalid hex after %. Surfaces as a
	// meaningful error, not a panic.
	f := &a2a.DefaultFetcher{}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "data:,%zz"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "percent-encoding")
}

func TestDefaultFetcher_HTTP_ContextCancelledPropagates(t *testing.T) {
	// A slow server + a pre-cancelled context: fetch must return
	// promptly with an error, not hang on the read.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &a2a.DefaultFetcher{}
	_, _, err := f.Fetch(ctx, a2a.Artifact{URI: srv.URL})
	require.Error(t, err)
}

func TestDefaultFetcher_HTTP_MalformedURL(t *testing.T) {
	// A URL with an invalid control byte fails at
	// http.NewRequestWithContext (before the network).
	f := &a2a.DefaultFetcher{}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "http://exam\x00ple.com/x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "build request")
}

func TestDefaultFetcher_HTTP_TooManySameOriginRedirects(t *testing.T) {
	// Same-origin redirect chain longer than the 5-hop limit
	// (10 hops here) should fail before storming the resolver.
	// Distinct from the cross-origin-refused test: same host, but
	// too many hops.
	mux := http.NewServeMux()
	hops := 0
	mux.HandleFunc("/r", func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, "/r", http.StatusFound) // self-redirect
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &a2a.DefaultFetcher{}
	_, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: srv.URL + "/r"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many redirects")
}

func TestDefaultFetcher_CheckSize_NegativeMaxSkipsCheck(t *testing.T) {
	// checkSize's negative-max short-circuit — exercised by
	// the Resolver path.
	res := &stubResolver{body: []byte(strings.Repeat("z", 64*1024))}
	f := &a2a.DefaultFetcher{Resolver: res, MaxBytes: -1}
	data, _, err := f.Fetch(context.Background(), a2a.Artifact{URI: "artifact://peer/x"})
	require.NoError(t, err)
	assert.Len(t, data, 64*1024)
}
