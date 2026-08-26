package email

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// loopbackTLS mints a throwaway self-signed certificate for
// 127.0.0.1 and returns the server config plus a root pool that
// trusts it. Everything stays in-process; no CA on the machine is
// consulted or modified.
func loopbackTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "rousseau-agent test imap"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}},
		MinVersion:   tls.VersionTLS12,
	}, pool
}

// literal adapts a byte slice to imap.LiteralReader for APPEND.
type literal struct{ *bytes.Reader }

func (l literal) Size() int64 { return l.Reader.Size() }

const (
	testIMAPUser = "agent@rousseau.example"
	testIMAPPass = "hunter2"
)

// startIMAPServer runs an in-memory IMAP server over TLS on
// loopback, seeded with the given raw RFC 5322 messages in INBOX.
// It returns the address and a root pool trusting the server.
func startIMAPServer(t *testing.T, raw ...string) (addr string, roots *x509.CertPool) {
	t.Helper()
	tlsConf, pool := loopbackTLS(t)

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testIMAPUser, testIMAPPass)
	require.NoError(t, user.Create("INBOX", nil))
	for _, msg := range raw {
		_, err := user.Append("INBOX", literal{bytes.NewReader([]byte(msg))},
			&imap.AppendOptions{Time: time.Unix(1_700_000_000, 0).UTC()})
		require.NoError(t, err)
	}
	mem.AddUser(user)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:   imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		Logger: discardLogger{},
	})
	t.Cleanup(func() { _ = srv.Close() }) //nolint:errcheck // test setup/teardown

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ln := tls.NewListener(tcp, tlsConf)
	go func() { _ = srv.Serve(ln) }() //nolint:errcheck // test setup/teardown

	return tcp.Addr().String(), pool
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// useTrustedDialer points the production factory's dial step at the
// loopback server for the duration of the test.
func useTrustedDialer(t *testing.T, roots *x509.CertPool) {
	t.Helper()
	old := imapDialTLS
	t.Cleanup(func() { imapDialTLS = old })
	imapDialTLS = func(addr string) (*imapclient.Client, error) {
		return imapclient.DialTLS(addr, &imapclient.Options{
			TLSConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		})
	}
}

const rawMail = "From: Alice <alice@example.test>\r\n" +
	"To: agent@rousseau.example\r\n" +
	"Subject: status please\r\n" +
	"Date: Tue, 14 Nov 2023 22:13:20 +0000\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"how are the builds?\r\n"

// TestDefaultIMAPFactory_PollsRealServer drives the whole production
// inbound path — TLS dial, LOGIN, SELECT, SEARCH UNSEEN, FETCH,
// STORE \Seen — against a real IMAP server implementation, so the
// imapAdapter/fetchAdapter/storeAdapter wiring is checked on the
// wire rather than against a stub.
func TestDefaultIMAPFactory_PollsRealServer(t *testing.T) {
	addr, roots := startIMAPServer(t, rawMail)
	useTrustedDialer(t, roots)

	var sentTo []string
	var sentBody []string
	c, err := New(Config{
		IMAPAddr: addr, IMAPUsername: testIMAPUser, IMAPPassword: testIMAPPass,
		SMTPAddr: "smtp.local:587", SMTPUsername: "u", SMTPPassword: "p",
		From: "agent@rousseau.example",
		SendMail: func(_, _ string, to []string, msg []byte, _, _ string) error {
			sentTo = append(sentTo, strings.Join(to, ","))
			sentBody = append(sentBody, string(msg))
			return nil
		},
	}, silentLogger())
	require.NoError(t, err)

	var seen []transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = append(seen, m)
		return "builds are green", nil
	})

	require.NoError(t, c.pollOnce(context.Background(), handler))
	require.Len(t, seen, 1)
	assert.Equal(t, "alice@example.test", seen[0].From)
	assert.Contains(t, seen[0].Body, "how are the builds?")
	require.Len(t, sentTo, 1)
	assert.Equal(t, "alice@example.test", sentTo[0])
	assert.Contains(t, sentBody[0], "builds are green")

	// The first poll marked the message \Seen, so a second poll finds
	// nothing and dispatches nothing.
	require.NoError(t, c.pollOnce(context.Background(), handler))
	assert.Len(t, seen, 1, "already-seen mail must not be re-delivered")
}

func TestDefaultIMAPFactory_WrongPasswordFailsLogin(t *testing.T) {
	addr, roots := startIMAPServer(t)
	useTrustedDialer(t, roots)

	_, err := defaultIMAPFactory(addr, testIMAPUser, "wrong-password")
	require.Error(t, err)
}

func TestDefaultIMAPFactory_UnreachableServerErrors(t *testing.T) {
	// Bind then immediately release a port so the address is valid
	// but nothing is listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	_, err = defaultIMAPFactory(addr, testIMAPUser, testIMAPPass)
	require.Error(t, err)
}

// TestIMAPDialTLS_UntrustedCertificateRejected exercises the
// production dialler itself (no injected TLS config): an untrusted
// self-signed certificate must fail verification.
func TestIMAPDialTLS_UntrustedCertificateRejected(t *testing.T) {
	addr, _ := startIMAPServer(t)
	_, err := imapDialTLS(addr)
	require.Error(t, err)
}

// TestPollOnce_SelectMissingMailboxOverRealServer covers the SELECT
// error path against the real protocol rather than a stub.
func TestPollOnce_SelectMissingMailboxOverRealServer(t *testing.T) {
	addr, roots := startIMAPServer(t)
	useTrustedDialer(t, roots)

	c, err := New(Config{
		IMAPAddr: addr, IMAPUsername: testIMAPUser, IMAPPassword: testIMAPPass,
		Mailbox:  "Archive",
		SMTPAddr: "smtp.local:587", SMTPUsername: "u", SMTPPassword: "p",
		From:     "agent@rousseau.example",
		SendMail: func(string, string, []string, []byte, string, string) error { return nil },
	}, silentLogger())
	require.NoError(t, err)

	err = c.pollOnce(context.Background(), echoHandler())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "email: select")
}

// TestStoreAdapter_NilCommandCloseIsSafe covers the defensive nil
// guard: some servers answer STORE without a command handle.
func TestStoreAdapter_NilCommandCloseIsSafe(t *testing.T) {
	assert.NoError(t, storeAdapter{}.Close())
}
