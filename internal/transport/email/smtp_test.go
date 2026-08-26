package email

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSMTP is a minimal RFC 5321 server on loopback: enough of the
// dialogue for net/smtp's SendMail to complete. It records the
// session so tests can assert on what was actually put on the wire
// rather than on a stubbed function call.
type fakeSMTP struct {
	addr string

	mu       sync.Mutex
	mailFrom string
	rcptTo   []string
	data     strings.Builder
	authSeen bool
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() }) //nolint:errcheck // test setup/teardown

	s := &fakeSMTP{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }() //nolint:errcheck // test setup/teardown
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	say := func(line string) {
		_, _ = w.WriteString(line + "\r\n") //nolint:errcheck // test setup/teardown
		_ = w.Flush()                       //nolint:errcheck // test setup/teardown
	}
	say("220 fake.local ESMTP ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			// No STARTTLS advertised: net/smtp then relies on the
			// localhost exemption in PlainAuth.
			say("250-fake.local")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(upper, "AUTH"):
			s.mu.Lock()
			s.authSeen = true
			s.mu.Unlock()
			say("235 2.7.0 authenticated")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			s.mu.Lock()
			s.mailFrom = strings.TrimPrefix(cmd[len("MAIL FROM:"):], " ")
			s.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			s.mu.Lock()
			s.rcptTo = append(s.rcptTo, strings.TrimPrefix(cmd[len("RCPT TO:"):], " "))
			s.mu.Unlock()
			say("250 ok")
		case upper == "DATA":
			say("354 send it")
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if dl == ".\r\n" {
					break
				}
				s.mu.Lock()
				s.data.WriteString(dl)
				s.mu.Unlock()
			}
			say("250 queued")
		case upper == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (s *fakeSMTP) session() (from string, rcpt []string, body string, authed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mailFrom, append([]string(nil), s.rcptTo...), s.data.String(), s.authSeen
}

// TestDefaultSendMail_DeliversOverSMTP drives the real net/smtp path
// against a loopback server, so the envelope, recipients and rendered
// body are checked on the wire.
func TestDefaultSendMail_DeliversOverSMTP(t *testing.T) {
	srv := startFakeSMTP(t)
	msg := buildMessage("bot@rousseau.example", "user@example.test", "hello there")

	err := defaultSendMail(srv.addr, "bot@rousseau.example",
		[]string{"user@example.test"}, msg, "user", "secret")
	require.NoError(t, err)

	from, rcpt, body, authed := srv.session()
	assert.Equal(t, "<bot@rousseau.example>", from)
	assert.Equal(t, []string{"<user@example.test>"}, rcpt)
	assert.True(t, authed, "PLAIN auth must be attempted")
	assert.Contains(t, body, "To: user@example.test")
	assert.Contains(t, body, "hello there")
}

// TestDeliver_EndToEndOverSMTP exercises Client.Deliver with the
// production SendMail (New installs defaultSendMail when none is
// configured).
func TestDeliver_EndToEndOverSMTP(t *testing.T) {
	srv := startFakeSMTP(t)
	c, err := New(Config{
		IMAPAddr: "imap.local:993", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: srv.addr, SMTPUsername: "u", SMTPPassword: "p",
		From:        "bot@rousseau.example",
		ReplyHeader: "[agent] ",
	}, silentLogger())
	require.NoError(t, err)

	require.NoError(t, c.Deliver(t.Context(), "user@example.test", "the answer"))

	_, rcpt, body, _ := srv.session()
	assert.Equal(t, []string{"<user@example.test>"}, rcpt)
	assert.Contains(t, body, "[agent] the answer")
}

func TestDefaultSendMail_RejectsAddrWithoutPort(t *testing.T) {
	err := defaultSendMail("smtp.example.test", "a@b.test", []string{"c@d.test"}, nil, "u", "p")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad SMTP addr")
}
