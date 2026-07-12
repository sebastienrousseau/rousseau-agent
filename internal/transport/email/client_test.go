package email

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---- fake IMAP client -----------------------------------------------

type fakeIMAP struct {
	messages   []*imapclient.FetchMessageBuffer
	seqNums    []uint32
	seenAdded  bool
	closeErr   error
}

func (f *fakeIMAP) Select(string, *imap.SelectOptions) (*imap.SelectData, error) {
	return &imap.SelectData{NumMessages: uint32(len(f.messages))}, nil
}
func (f *fakeIMAP) Search(*imap.SearchCriteria, *imap.SearchOptions) (*imap.SearchData, error) {
	set := imap.SeqSet{}
	set.AddNum(f.seqNums...)
	return &imap.SearchData{All: set}, nil
}
func (f *fakeIMAP) Fetch(imap.NumSet, *imap.FetchOptions) FetchCommand {
	return &fakeFetch{buffers: f.messages}
}
func (f *fakeIMAP) Store(imap.NumSet, *imap.StoreFlags, *imap.StoreOptions) StoreCommand {
	f.seenAdded = true
	return &fakeStore{}
}
func (f *fakeIMAP) Close() error { return f.closeErr }

type fakeFetch struct{ buffers []*imapclient.FetchMessageBuffer }

func (f *fakeFetch) Collect() ([]*imapclient.FetchMessageBuffer, error) { return f.buffers, nil }
func (f *fakeFetch) Close() error                                       { return nil }

type fakeStore struct{}

func (f *fakeStore) Close() error { return nil }

// mkMessage constructs a fetched-message buffer with sensible defaults.
func mkMessage(from, subject, body string) *imapclient.FetchMessageBuffer {
	msg := &imapclient.FetchMessageBuffer{
		Envelope: &imap.Envelope{
			Subject: subject,
			Date:    time.Unix(1_700_000_000, 0).UTC(),
			From: []imap.Address{{
				Mailbox: strings.SplitN(from, "@", 2)[0],
				Host:    strings.SplitN(from, "@", 2)[1],
			}},
		},
	}
	if body != "" {
		msg.BodySection = []imapclient.FetchBodySectionBuffer{{
			Section: &imap.FetchItemBodySection{},
			Bytes:   []byte("Subject: " + subject + "\r\n\r\n" + body),
		}}
	}
	return msg
}

// mkClient wraps New with the given fakes.
func mkClient(t *testing.T, imapFake *fakeIMAP, sentTo *[]string, sentBody *[]string) *Client {
	t.Helper()
	c, err := New(Config{
		IMAPAddr: "imap.local:993", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "smtp.local:587", SMTPUsername: "u", SMTPPassword: "p",
		From: "bot@rousseau.example",
		IMAPClientFactory: func(string, string, string) (IMAPClient, error) {
			return imapFake, nil
		},
		SendMail: func(_, _ string, to []string, msg []byte, _, _ string) error {
			if sentTo != nil {
				*sentTo = append(*sentTo, strings.Join(to, ","))
			}
			if sentBody != nil {
				*sentBody = append(*sentBody, string(msg))
			}
			return nil
		},
	}, silentLogger())
	require.NoError(t, err)
	return c
}

// ---- constructor tests ----------------------------------------------

func TestNew_RequiresIMAP(t *testing.T) {
	_, err := New(Config{SMTPAddr: "s:1", SMTPUsername: "u", SMTPPassword: "p", From: "a@b"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_RequiresSMTP(t *testing.T) {
	_, err := New(Config{IMAPAddr: "i:1", IMAPUsername: "u", IMAPPassword: "p", From: "a@b"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_RequiresFrom(t *testing.T) {
	_, err := New(Config{
		IMAPAddr: "i:1", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "s:1", SMTPUsername: "u", SMTPPassword: "p",
	}, silentLogger())
	assert.Error(t, err)
}

func TestNew_DefaultsMailboxAndPoll(t *testing.T) {
	c, err := New(Config{
		IMAPAddr: "i:1", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "s:1", SMTPUsername: "u", SMTPPassword: "p",
		From: "bot@x",
	}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "INBOX", c.cfg.Mailbox)
	assert.NotZero(t, c.cfg.PollInterval)
	assert.Equal(t, "email", c.Name())
}

// ---- send/receive tests --------------------------------------------

func TestDeliver_CallsSendMailWithMessage(t *testing.T) {
	var sentTo, sentBody []string
	c := mkClient(t, &fakeIMAP{}, &sentTo, &sentBody)
	require.NoError(t, c.Deliver(context.Background(), "user@example.com", "hello world"))
	require.Len(t, sentTo, 1)
	assert.Equal(t, "user@example.com", sentTo[0])
	require.Len(t, sentBody, 1)
	assert.Contains(t, sentBody[0], "To: user@example.com")
	assert.Contains(t, sentBody[0], "hello world")
	assert.Contains(t, sentBody[0], "Subject: rousseau-agent reply")
}

func TestDeliver_PrependsReplyHeader(t *testing.T) {
	var sentBody []string
	c, err := New(Config{
		IMAPAddr: "i:1", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "s:1", SMTPUsername: "u", SMTPPassword: "p",
		From: "bot@x", ReplyHeader: "[bot] ",
		IMAPClientFactory: func(string, string, string) (IMAPClient, error) { return &fakeIMAP{}, nil },
		SendMail: func(_, _ string, _ []string, msg []byte, _, _ string) error {
			sentBody = append(sentBody, string(msg))
			return nil
		},
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "u@x", "body"))
	assert.Contains(t, sentBody[0], "[bot] body")
}

func TestDeliver_SMTPErrorSurfaces(t *testing.T) {
	c, err := New(Config{
		IMAPAddr: "i:1", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "s:1", SMTPUsername: "u", SMTPPassword: "p",
		From: "bot@x",
		IMAPClientFactory: func(string, string, string) (IMAPClient, error) { return &fakeIMAP{}, nil },
		SendMail: func(string, string, []string, []byte, string, string) error {
			return errors.New("smtp connect refused")
		},
	}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "u@x", "hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect refused")
}

func TestPollOnce_HandlesUnseenAndMarksSeen(t *testing.T) {
	fake := &fakeIMAP{
		seqNums:  []uint32{1},
		messages: []*imapclient.FetchMessageBuffer{mkMessage("alice@example.com", "hi", "message body")},
	}
	var sentTo, sentBody []string
	c := mkClient(t, fake, &sentTo, &sentBody)

	handled := false
	err := c.pollOnce(context.Background(), transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		handled = true
		assert.Equal(t, "alice@example.com", m.From)
		assert.Equal(t, "message body", m.Body)
		return "reply body", nil
	}))
	require.NoError(t, err)
	assert.True(t, handled)
	assert.True(t, fake.seenAdded, "message should have been flagged as seen")
	require.Len(t, sentBody, 1)
	assert.Contains(t, sentBody[0], "reply body")
}

func TestPollOnce_HandlerErrorSkipsReply(t *testing.T) {
	fake := &fakeIMAP{
		seqNums:  []uint32{1},
		messages: []*imapclient.FetchMessageBuffer{mkMessage("alice@example.com", "hi", "hello")},
	}
	sendCount := 0
	c, err := New(Config{
		IMAPAddr: "i:1", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "s:1", SMTPUsername: "u", SMTPPassword: "p",
		From: "bot@x",
		IMAPClientFactory: func(string, string, string) (IMAPClient, error) { return fake, nil },
		SendMail: func(string, string, []string, []byte, string, string) error {
			sendCount++
			return nil
		},
	}, silentLogger())
	require.NoError(t, err)

	err = c.pollOnce(context.Background(), transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", errors.New("boom")
	}))
	require.NoError(t, err)
	assert.Zero(t, sendCount)
}

func TestPollOnce_EmptyMailboxIsNoop(t *testing.T) {
	fake := &fakeIMAP{seqNums: nil}
	c := mkClient(t, fake, nil, nil)
	require.NoError(t, c.pollOnce(context.Background(), transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		t.Fatal("handler must not fire when no UNSEEN mail")
		return "", nil
	})))
}

func TestPollOnce_ConnectErrorSurfaces(t *testing.T) {
	c, err := New(Config{
		IMAPAddr: "i:1", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "s:1", SMTPUsername: "u", SMTPPassword: "p",
		From: "bot@x",
		IMAPClientFactory: func(string, string, string) (IMAPClient, error) {
			return nil, errors.New("dial refused")
		},
	}, silentLogger())
	require.NoError(t, err)
	err = c.pollOnce(context.Background(), transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect")
}

func TestEnvelopeFrom(t *testing.T) {
	m := &imapclient.FetchMessageBuffer{Envelope: &imap.Envelope{From: []imap.Address{{Mailbox: "alice", Host: "example.com"}}}}
	assert.Equal(t, "alice@example.com", envelopeFrom(m))
}

func TestEnvelopeFrom_MissingIsEmpty(t *testing.T) {
	assert.Empty(t, envelopeFrom(nil))
	assert.Empty(t, envelopeFrom(&imapclient.FetchMessageBuffer{}))
}

func TestExtractBody_HandlesHeaderSeparation(t *testing.T) {
	m := &imapclient.FetchMessageBuffer{
		BodySection: []imapclient.FetchBodySectionBuffer{{
			Section: &imap.FetchItemBodySection{},
			Bytes:   []byte("Subject: hi\r\n\r\nBody content\r\n"),
		}},
	}
	assert.Equal(t, "Body content", extractBody(m))
}

func TestExtractBody_EmptyReturnsEmpty(t *testing.T) {
	assert.Empty(t, extractBody(nil))
	assert.Empty(t, extractBody(&imapclient.FetchMessageBuffer{}))
}

func TestBuildMessage_ContainsHeadersAndBody(t *testing.T) {
	msg := buildMessage("bot@x", "user@y", "hello")
	s := string(msg)
	assert.Contains(t, s, "From: bot@x")
	assert.Contains(t, s, "To: user@y")
	assert.Contains(t, s, "Subject: rousseau-agent reply")
	assert.Contains(t, s, "\r\n\r\nhello\r\n")
}

func TestSplitHostPort(t *testing.T) {
	host, port, ok := splitHostPort("smtp.example.com:587")
	assert.True(t, ok)
	assert.Equal(t, "smtp.example.com", host)
	assert.Equal(t, "587", port)

	_, _, ok = splitHostPort("noport")
	assert.False(t, ok)
}

// ---- lifecycle tests -----------------------------------------------

func TestStart_HandlerNilErrors(t *testing.T) {
	c, err := New(Config{
		IMAPAddr: "i:1", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "s:1", SMTPUsername: "u", SMTPPassword: "p",
		From: "bot@x",
	}, silentLogger())
	require.NoError(t, err)
	err = c.Start(context.Background(), nil)
	assert.Error(t, err)
}

func TestStopIdempotent(t *testing.T) {
	c, err := New(Config{
		IMAPAddr: "i:1", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "s:1", SMTPUsername: "u", SMTPPassword: "p",
		From: "bot@x",
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Stop())
	require.NoError(t, c.Stop())
}
