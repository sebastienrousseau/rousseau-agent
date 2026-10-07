package email

import (
	"context"
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// A reply is filed under the inbound mail: "Re: " subject and
// In-Reply-To / References carrying the inbound Message-ID.
func TestPollOnce_ReplyIsThreaded(t *testing.T) {
	m := mkMessage("alice@example.com", "Quarterly numbers", "please summarise")
	m.Envelope.MessageID = "<abc123@example.com>"
	fake := &fakeIMAP{seqNums: []uint32{1}, messages: []*imapclient.FetchMessageBuffer{m}}
	var sentTo, sentBody []string
	c := mkClient(t, fake, &sentTo, &sentBody)

	var seen transport.IncomingMessage
	err := c.pollOnce(context.Background(), transport.HandlerFunc(func(_ context.Context, msg transport.IncomingMessage) (string, error) {
		seen = msg
		return "summary", nil
	}))
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", seen.Conversation)
	assert.Equal(t, "<abc123@example.com>", seen.MessageID)
	assert.Equal(t, "Quarterly numbers", seen.Thread)
	require.Len(t, sentBody, 1)
	assert.Contains(t, sentBody[0], "Subject: Re: Quarterly numbers\r\n")
	assert.Contains(t, sentBody[0], "In-Reply-To: <abc123@example.com>\r\n")
	assert.Contains(t, sentBody[0], "References: <abc123@example.com>\r\n")
}

func TestReplySubject(t *testing.T) {
	assert.Equal(t, defaultSubject, replySubject(""))
	assert.Equal(t, defaultSubject, replySubject("  "))
	assert.Equal(t, "Re: hi", replySubject("hi"))
	assert.Equal(t, "Re: hi", replySubject("Re: hi"))
	assert.Equal(t, "RE: hi", replySubject("RE: hi"))
}

// A crafted subject or Message-ID cannot inject headers into the
// reply.
func TestBuildMessage_SanitisesInjectedHeaders(t *testing.T) {
	msg := string(buildMessage("bot@x", "user@y", "Re: hi\r\nBcc: evil@z", "<id>\nX-Evil: 1", "body"))
	assert.Contains(t, msg, "Subject: Re: hiBcc: evil@z\r\n")
	assert.NotContains(t, msg, "\r\nBcc:")
	assert.NotContains(t, msg, "\nX-Evil")
	assert.Contains(t, msg, "In-Reply-To: <id>X-Evil: 1\r\n")

	plain := string(buildMessage("bot@x", "user@y", defaultSubject, "", "body"))
	assert.NotContains(t, plain, "In-Reply-To")
}
