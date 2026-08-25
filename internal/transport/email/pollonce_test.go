package email

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// scriptedIMAP is a fakeIMAP with injectable failures for each step
// of the poll sequence.
type scriptedIMAP struct {
	fakeIMAP
	selectErr  error
	searchErr  error
	collectErr error
	stored     bool
}

func (s *scriptedIMAP) Select(name string, opts *imap.SelectOptions) (*imap.SelectData, error) {
	if s.selectErr != nil {
		return nil, s.selectErr
	}
	return s.fakeIMAP.Select(name, opts)
}

func (s *scriptedIMAP) Search(c *imap.SearchCriteria, o *imap.SearchOptions) (*imap.SearchData, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.fakeIMAP.Search(c, o)
}

func (s *scriptedIMAP) Fetch(set imap.NumSet, opts *imap.FetchOptions) FetchCommand {
	if s.collectErr != nil {
		return &errFetch{err: s.collectErr}
	}
	return s.fakeIMAP.Fetch(set, opts)
}

func (s *scriptedIMAP) Store(set imap.NumSet, f *imap.StoreFlags, o *imap.StoreOptions) StoreCommand {
	s.stored = true
	return s.fakeIMAP.Store(set, f, o)
}

type errFetch struct{ err error }

func (e *errFetch) Collect() ([]*imapclient.FetchMessageBuffer, error) { return nil, e.err }
func (e *errFetch) Close() error                                       { return nil }

// bufLogger returns a logger writing into buf so tests can assert on
// the messages the poll loop emits for non-fatal failures.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func clientWith(t *testing.T, cfg Config, logger *slog.Logger) *Client {
	t.Helper()
	if cfg.IMAPAddr == "" {
		cfg.IMAPAddr, cfg.IMAPUsername, cfg.IMAPPassword = "imap.local:993", "u", "p"
	}
	if cfg.SMTPAddr == "" {
		cfg.SMTPAddr, cfg.SMTPUsername, cfg.SMTPPassword = "smtp.local:587", "u", "p"
	}
	if cfg.From == "" {
		cfg.From = "bot@rousseau.example"
	}
	if cfg.SendMail == nil {
		cfg.SendMail = func(string, string, []string, []byte, string, string) error { return nil }
	}
	c, err := New(cfg, logger)
	require.NoError(t, err)
	return c
}

func echoHandler() transport.Handler {
	return transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		return "re: " + m.Body, nil
	})
}

func TestPollOnce_SelectErrorSurfaces(t *testing.T) {
	fake := &scriptedIMAP{selectErr: errors.New("mailbox missing")}
	c := clientWith(t, Config{IMAPClientFactory: func(string, string, string) (IMAPClient, error) {
		return fake, nil
	}}, silentLogger())

	err := c.pollOnce(context.Background(), echoHandler())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "email: select")
	assert.False(t, fake.stored, "nothing may be marked seen when SELECT fails")
}

func TestPollOnce_SearchErrorSurfaces(t *testing.T) {
	fake := &scriptedIMAP{searchErr: errors.New("search unsupported")}
	c := clientWith(t, Config{IMAPClientFactory: func(string, string, string) (IMAPClient, error) {
		return fake, nil
	}}, silentLogger())

	err := c.pollOnce(context.Background(), echoHandler())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "email: search")
	assert.False(t, fake.stored)
}

func TestPollOnce_FetchErrorSurfaces(t *testing.T) {
	fake := &scriptedIMAP{collectErr: errors.New("connection reset")}
	fake.seqNums = []uint32{1}
	fake.messages = []*imapclient.FetchMessageBuffer{mkMessage("a@b.example", "hi", "body")}
	c := clientWith(t, Config{IMAPClientFactory: func(string, string, string) (IMAPClient, error) {
		return fake, nil
	}}, silentLogger())

	err := c.pollOnce(context.Background(), echoHandler())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "email: fetch")
	assert.False(t, fake.stored, "a failed fetch must not mark messages seen")
}

// TestPollOnce_SubjectOnlyMailUsesSubjectAsBody covers mail with an
// empty body — the subject line carries the whole request.
func TestPollOnce_SubjectOnlyMailUsesSubjectAsBody(t *testing.T) {
	fake := &scriptedIMAP{}
	fake.seqNums = []uint32{1}
	fake.messages = []*imapclient.FetchMessageBuffer{mkMessage("a@b.example", "what is the time", "")}

	var seen []transport.IncomingMessage
	c := clientWith(t, Config{IMAPClientFactory: func(string, string, string) (IMAPClient, error) {
		return fake, nil
	}}, silentLogger())

	require.NoError(t, c.pollOnce(context.Background(),
		transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
			seen = append(seen, m)
			return "", nil
		})))
	require.Len(t, seen, 1)
	assert.Equal(t, "what is the time", seen[0].Body)
}

// TestPollOnce_UnaddressableMailIsSkipped: a message with neither a
// usable From nor any content must not reach the handler, but the
// batch is still marked seen so we do not re-read it forever.
func TestPollOnce_UnaddressableMailIsSkipped(t *testing.T) {
	fake := &scriptedIMAP{}
	fake.seqNums = []uint32{1, 2}
	fake.messages = []*imapclient.FetchMessageBuffer{
		{},                                     // no envelope, no body at all
		mkMessage("a@b.example", "", "  \r\n"), // has a sender but nothing to say
	}

	called := 0
	c := clientWith(t, Config{IMAPClientFactory: func(string, string, string) (IMAPClient, error) {
		return fake, nil
	}}, silentLogger())

	require.NoError(t, c.pollOnce(context.Background(),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			called++
			return "", nil
		})))
	assert.Zero(t, called)
	assert.True(t, fake.stored, "skipped mail must still be marked seen")
}

func TestPollOnce_DeliverFailureIsLoggedAndPollContinues(t *testing.T) {
	fake := &scriptedIMAP{}
	fake.seqNums = []uint32{1, 2}
	fake.messages = []*imapclient.FetchMessageBuffer{
		mkMessage("first@b.example", "s1", "one"),
		mkMessage("second@b.example", "s2", "two"),
	}

	var logs bytes.Buffer
	attempts := 0
	c := clientWith(t, Config{
		IMAPClientFactory: func(string, string, string) (IMAPClient, error) { return fake, nil },
		SendMail: func(string, string, []string, []byte, string, string) error {
			attempts++
			return errors.New("smtp: 550 rejected")
		},
	}, bufLogger(&logs))

	require.NoError(t, c.pollOnce(context.Background(), echoHandler()))
	assert.Equal(t, 2, attempts, "a failed send must not abort the rest of the batch")
	assert.Contains(t, logs.String(), "email.send_failed")
	assert.True(t, fake.stored)
}

func TestEnvelopeFrom_BareMailboxWithoutHost(t *testing.T) {
	m := &imapclient.FetchMessageBuffer{
		Envelope: &imap.Envelope{From: []imap.Address{{Mailbox: "postmaster"}}},
	}
	assert.Equal(t, "postmaster", envelopeFrom(m))
}

func TestEnvelopeFrom_NilBuffer(t *testing.T) {
	assert.Empty(t, envelopeFrom(nil))
}

// TestExtractBody_SkipsEmptySections: servers may return a zero-byte
// section ahead of the real one; the first non-empty wins.
func TestExtractBody_SkipsEmptySections(t *testing.T) {
	m := &imapclient.FetchMessageBuffer{
		BodySection: []imapclient.FetchBodySectionBuffer{
			{Section: &imap.FetchItemBodySection{}, Bytes: nil},
			{Section: &imap.FetchItemBodySection{}, Bytes: []byte("Subject: x\r\n\r\nreal body\r\n")},
		},
	}
	assert.Equal(t, "real body", extractBody(m))
}

func TestExtractBody_NilBuffer(t *testing.T) {
	assert.Empty(t, extractBody(nil))
}

func TestNew_NilLoggerFallsBackToDefault(t *testing.T) {
	c := clientWith(t, Config{}, nil)
	assert.NotNil(t, c.logger)
}
