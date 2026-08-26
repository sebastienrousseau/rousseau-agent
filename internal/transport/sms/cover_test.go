package sms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_RequiresProvider(t *testing.T) {
	_, err := New(Config{From: "+15550000000"}, silentLogger())
	require.Error(t, err)
	assert.ErrorContains(t, err, "Provider is required")
}

func TestNew_NilLoggerFallsBackToDefault(t *testing.T) {
	c, err := New(Config{
		Provider:   ProviderTwilio,
		From:       "+15550000000",
		AccountSID: "AC1",
		AuthToken:  "tok",
	}, nil)
	require.NoError(t, err)
	assert.NotNil(t, c.logger)
	assert.Equal(t, "sms-twilio", c.Name())
}

// TestDeliver_UnknownProviderAtSendTime guards the defensive branch that
// fires if Config.Provider is mutated after construction.
func TestDeliver_UnknownProviderAtSendTime(t *testing.T) {
	c, err := New(Config{
		Provider:   ProviderTwilio,
		From:       "+15550000000",
		AccountSID: "AC1",
		AuthToken:  "tok",
	}, silentLogger())
	require.NoError(t, err)
	c.cfg.Provider = Provider("carrier-pigeon")

	err = c.Deliver(context.Background(), "+15551112222", "hi")
	require.Error(t, err)
	assert.ErrorContains(t, err, `unknown provider "carrier-pigeon"`)
}

func TestDeliver_ErrorPaths(t *testing.T) {
	truncated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()                                            //nolint:errcheck // fixture
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8192\r\n\r\nshort") //nolint:errcheck // fixture
		_ = buf.Flush()                                                                //nolint:errcheck // fixture
	}))
	defer truncated.Close()

	tests := []struct {
		name    string
		cfg     Config
		client  *http.Client
		wantErr string
	}{
		{
			name: "twilio request cannot be built",
			cfg: Config{
				Provider: ProviderTwilio, From: "+1555", AccountSID: "AC1", AuthToken: "tok",
				BaseURL: "http://exa\x7fmple.invalid",
			},
			wantErr: "sms: twilio: build request",
		},
		{
			name: "vonage request cannot be built",
			cfg: Config{
				Provider: ProviderVonage, From: "+1555", APIKey: "key", AuthToken: "secret",
				BaseURL: "http://exa\x7fmple.invalid",
			},
			wantErr: "sms: vonage: build request",
		},
		{
			name: "twilio transport failure",
			cfg: Config{
				Provider: ProviderTwilio, From: "+1555", AccountSID: "AC1", AuthToken: "tok",
				BaseURL: "http://127.0.0.1:1",
			},
			wantErr: "sms: twilio",
		},
		{
			name: "vonage truncated response body",
			cfg: Config{
				Provider: ProviderVonage, From: "+1555", APIKey: "key", AuthToken: "secret",
				BaseURL: truncated.URL,
			},
			client:  truncated.Client(),
			wantErr: "sms: vonage: read",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.HTTPClient = tc.client
			if cfg.HTTPClient == nil {
				cfg.HTTPClient = &http.Client{Timeout: 2 * time.Second}
			}
			c, err := New(cfg, silentLogger())
			require.NoError(t, err)
			err = c.Deliver(context.Background(), "+15551112222", "hi")
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestVonage_NonJSONBodyIsAccepted proves an unparseable success body is
// not treated as a carrier error.
func TestVonage_NonJSONBodyIsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>ok</html>`)) //nolint:errcheck // fixture
	}))
	defer srv.Close()

	c, err := New(Config{
		Provider: ProviderVonage, From: "+1555", APIKey: "key", AuthToken: "secret",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	assert.NoError(t, c.Deliver(context.Background(), "+15551112222", "hi"))
}
