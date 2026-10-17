package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
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
// it. allow and origin are fields so in-package tests can stand an
// httptest server in for Slack.
type fileFetcher struct {
	client *http.Client
	allow  *regexp.Regexp // https, exactly files.slack.com, no port or userinfo
	origin string         // what allow accepts, for the refusal message
	limit  int64
}

// newFileFetcher copies base (so the caller's client is not mutated)
// and installs the redirect policy on the copy.
func newFileFetcher(base *http.Client) fileFetcher {
	fc := *base
	fc.CheckRedirect = refuseCrossHostRedirect
	return fileFetcher{
		client: &fc,
		allow:  transport.FetchPattern("https", slackFileHost),
		origin: "https://" + slackFileHost,
		limit:  maxFileBody,
	}
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
	if !c.files.allow.MatchString(rawURL) {
		return nil, fmt.Errorf("slack: file GET: refused, not a %s URL", c.files.origin)
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
