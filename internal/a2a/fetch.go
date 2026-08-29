package a2a

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Fetcher resolves an [Artifact.URI] to its bytes + content type.
// Implementations decide which URI schemes they accept; unknown
// schemes must return an error mentioning the scheme so operators
// can enable it or reject the artifact.
//
// The three canonical A2A schemes and their intended handlers:
//
//   - `data:...`      inline base64/percent-encoded blob. RFC 2397
//     "data URL scheme." Suitable for artifacts smaller
//     than ~1 MiB — no round-trip, but grows the task
//     envelope on the wire.
//   - `http(s)://...` pre-signed URL fetched via a bounded HTTP GET.
//     Suitable for artifacts up to the caller's
//     MaxBytes. The recipient MUST NOT follow
//     redirects across origins unattended (mitigated by
//     Client.CheckRedirect below).
//   - `artifact://<agent-id>/<opaque-id>` A2A-native reference to
//     an artifact held by the sending agent's own store. Resolved
//     via the caller-supplied Resolver so peers can implement
//     their own storage transport (S3, local FS, RPC) without
//     baking it into this package.
//
// See [`docs/a2a.md`](../../docs/a2a.md) for the on-wire choices +
// interop expectations.
type Fetcher interface {
	Fetch(ctx context.Context, a Artifact) (data []byte, contentType string, err error)
}

// Resolver is the caller-supplied hook for `artifact://` URIs. A
// peer that hosts its own artifact store implements this; the
// DefaultFetcher dispatches artifact:// URIs to it.
//
// Returning (nil, "", err) surfaces the error to the caller. A
// resolver that does not implement `artifact://` should return
// [ErrUnsupportedScheme].
type Resolver interface {
	Resolve(ctx context.Context, uri string) (data []byte, contentType string, err error)
}

// ErrUnsupportedScheme is returned when an [Artifact.URI] uses a
// scheme the fetcher does not know how to handle. Errors returned
// from a Fetcher wrap this so callers can `errors.Is` on it.
var ErrUnsupportedScheme = errors.New("a2a: unsupported artifact URI scheme")

// ErrArtifactTooLarge is returned when the resolved bytes exceed
// the caller's MaxBytes. Wraps a size in the message so operators
// can tune the cap.
var ErrArtifactTooLarge = errors.New("a2a: artifact exceeds MaxBytes")

// DefaultFetcher is the shipped implementation. It handles data:
// and http(s):// natively; artifact:// URIs are dispatched to the
// Resolver (when set). Set MaxBytes to bound resolved size — zero
// falls back to 32 MiB, which is the largest artifact size the
// spec suggests as an interop target.
//
// The zero value is usable: DefaultFetcher{} handles data: and
// http(s):// against the default http.Client, with a 32 MiB cap
// and no artifact:// support.
type DefaultFetcher struct {
	// Client is the http.Client used for http(s) URIs. Nil uses
	// [DefaultHTTPClient], which disables cross-origin redirects
	// and sets a 30s timeout.
	Client *http.Client
	// Resolver handles `artifact://` URIs. Nil means artifact://
	// returns ErrUnsupportedScheme.
	Resolver Resolver
	// MaxBytes is the per-artifact size cap. Zero uses 32 MiB.
	// Negative disables the cap (only sane in tests).
	MaxBytes int64
}

// DefaultMaxBytes is the shipped per-artifact fetch cap.
const DefaultMaxBytes int64 = 32 * 1024 * 1024

// DefaultHTTPClient is the http.Client used when
// DefaultFetcher.Client is nil. Disables cross-origin redirects
// (a pre-signed URL that redirects to a different host is a
// classic SSRF vector), sets a 30s total timeout, and reuses the
// default transport.
var DefaultHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return nil
		}
		if req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("a2a: cross-origin redirect refused: %s → %s", via[0].URL.Host, req.URL.Host)
		}
		if len(via) >= 5 {
			return errors.New("a2a: too many redirects")
		}
		return nil
	},
}

// Fetch satisfies [Fetcher]. Dispatches on the URI scheme:
//
//   - "data:"                     → decodeDataURI (RFC 2397)
//   - "http:" / "https:"          → HTTP GET via f.Client
//   - "artifact:"                 → f.Resolver.Resolve
//   - anything else               → ErrUnsupportedScheme
//
// Enforces the size cap in the two branches that could return
// arbitrary bytes from a peer (HTTP + Resolver); data: is bounded
// by the artifact envelope itself so no re-check is needed.
func (f *DefaultFetcher) Fetch(ctx context.Context, a Artifact) ([]byte, string, error) {
	if a.URI == "" {
		return nil, "", errors.New("a2a: artifact URI is empty")
	}
	scheme, _, _ := strings.Cut(a.URI, ":")
	switch strings.ToLower(scheme) {
	case "data":
		return decodeDataURI(a.URI, f.effectiveMax())
	case "http", "https":
		return f.fetchHTTP(ctx, a.URI)
	case "artifact":
		if f.Resolver == nil {
			return nil, "", fmt.Errorf("%w: artifact:// (no Resolver configured)", ErrUnsupportedScheme)
		}
		data, ct, err := f.Resolver.Resolve(ctx, a.URI)
		if err != nil {
			return nil, "", fmt.Errorf("a2a: resolve %s: %w", a.URI, err)
		}
		if err := f.checkSize(int64(len(data))); err != nil {
			return nil, "", err
		}
		return data, ct, nil
	default:
		return nil, "", fmt.Errorf("%w: %s", ErrUnsupportedScheme, scheme)
	}
}

func (f *DefaultFetcher) effectiveMax() int64 {
	if f.MaxBytes == 0 {
		return DefaultMaxBytes
	}
	return f.MaxBytes
}

// checkSize enforces the MaxBytes cap. A negative MaxBytes disables
// the cap (test-only sanity — production always caps).
func (f *DefaultFetcher) checkSize(n int64) error {
	limit := f.effectiveMax()
	if limit < 0 {
		return nil
	}
	if n > limit {
		return fmt.Errorf("%w: %d > %d", ErrArtifactTooLarge, n, limit)
	}
	return nil
}

// decodeDataURI parses an RFC 2397 data: URI and returns its
// decoded bytes + declared MIME type (or "application/octet-stream"
// when unspecified). Enforces the size cap during decode so a
// hostile 100 MB base64 payload does not allocate before rejection.
func decodeDataURI(uri string, max int64) ([]byte, string, error) {
	// Expected layout: data:[<mediatype>][;base64],<payload>
	rest, ok := strings.CutPrefix(uri, "data:")
	if !ok {
		return nil, "", errors.New("a2a: not a data URI")
	}
	head, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return nil, "", errors.New("a2a: data URI missing comma separator")
	}
	mediaType := "application/octet-stream"
	base64Encoded := false
	if head != "" {
		parts := strings.Split(head, ";")
		if !strings.EqualFold(parts[0], "base64") {
			mediaType = parts[0]
		}
		for _, p := range parts {
			if strings.EqualFold(p, "base64") {
				base64Encoded = true
				break
			}
		}
	}
	// Fast size guard using encoded length before doing the work.
	if max >= 0 && int64(len(payload)) > max*2 {
		return nil, "", fmt.Errorf("%w: encoded=%d cap=%d", ErrArtifactTooLarge, len(payload), max)
	}
	var out []byte
	var err error
	if base64Encoded {
		out, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, "", fmt.Errorf("a2a: decode base64: %w", err)
		}
	} else {
		// Percent-decoded payload (RFC 2397 allows either).
		out, err = urlDecodeBytes(payload)
		if err != nil {
			return nil, "", err
		}
	}
	if max >= 0 && int64(len(out)) > max {
		return nil, "", fmt.Errorf("%w: %d > %d", ErrArtifactTooLarge, len(out), max)
	}
	return out, mediaType, nil
}

// urlDecodeBytes handles percent-encoding for non-base64 data: URIs.
// net/url.QueryUnescape is close enough for our purposes (RFC 3986
// vs 3987 differences don't matter here — the payload is bytes).
func urlDecodeBytes(s string) ([]byte, error) {
	dec, err := url.QueryUnescape(s)
	if err != nil {
		return nil, fmt.Errorf("a2a: decode percent-encoding: %w", err)
	}
	return []byte(dec), nil
}

// fetchHTTP performs a bounded HTTP GET. Reads at most MaxBytes+1
// so we can distinguish "at limit" from "over limit" cleanly.
func (f *DefaultFetcher) fetchHTTP(ctx context.Context, uri string) ([]byte, string, error) {
	client := f.Client
	if client == nil {
		client = DefaultHTTPClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, "", fmt.Errorf("a2a: build request %s: %w", uri, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("a2a: fetch %s: %w", uri, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("a2a: fetch %s: HTTP %d", uri, resp.StatusCode)
	}
	limit := f.effectiveMax()
	var reader io.Reader = resp.Body
	if limit >= 0 {
		reader = io.LimitReader(resp.Body, limit+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", fmt.Errorf("a2a: read %s: %w", uri, err)
	}
	if err := f.checkSize(int64(len(data))); err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}
