package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// slackFileHost is the only host the bot token is sent to for a file
// download. url_private_download comes from the event payload, so
// without this check a crafted URL would receive the token.
const slackFileHost = "files.slack.com"

// maxFileRedirects caps same-host redirects on a file download.
const maxFileRedirects = 5

// fileFetcher downloads url_private_download files with the bot token,
// only from https://files.slack.com, refusing redirects off that host
// and treating a body over limit as an error rather than truncating
// it. scheme and host are fields so in-package tests can stand an
// httptest server in for Slack.
type fileFetcher struct {
	client *http.Client
	scheme string
	host   string
	limit  int64
}

// newFileFetcher copies base (so the caller's client is not mutated)
// and installs the redirect policy on the copy.
func newFileFetcher(base *http.Client) fileFetcher {
	fc := *base
	fc.CheckRedirect = refuseCrossHostRedirect
	return fileFetcher{client: &fc, scheme: "https", host: slackFileHost, limit: maxFileBody}
}

// allowed reports whether raw is a URL the bot token may be sent to:
// the configured scheme and exactly the configured host (no port, no
// userinfo, no look-alike suffix).
func (f fileFetcher) allowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == f.scheme && u.Host == f.host && u.User == nil
}

// refuseCrossHostRedirect stops a redirect that leaves the original
// scheme and host. Go already drops Authorization on a cross-host
// redirect; refusing it outright also means no request reaches the
// other host at all.
func refuseCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxFileRedirects {
		return errors.New("slack: file GET: too many redirects")
	}
	first := via[0].URL
	if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return errors.New("slack: file GET: refused redirect to another host")
	}
	return nil
}

// downloadFile GETs the file URL with the bot token and returns the
// bytes. Slack requires the Authorization header for
// url_private_download; a bare GET returns HTML.
func (c *Client) downloadFile(ctx context.Context, rawURL string) ([]byte, error) {
	if !c.files.allowed(rawURL) {
		return nil, fmt.Errorf("slack: file GET: refused, not a %s://%s URL", c.files.scheme, c.files.host)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("slack: build file GET: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.BotToken)
	resp, err := c.files.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack: file GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("slack: file GET: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.files.limit+1))
	if err != nil {
		return nil, fmt.Errorf("slack: file GET: read: %w", err)
	}
	if int64(len(body)) > c.files.limit {
		return nil, fmt.Errorf("slack: file GET: body exceeds %d bytes", c.files.limit)
	}
	return body, nil
}
