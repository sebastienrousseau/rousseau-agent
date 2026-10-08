package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// An allowlist keyed on the forgeable From header refuses to start
// unless the operator authenticates mail or explicitly opts out.
func TestValidateEmailAuth(t *testing.T) {
	allow := []string{"alice@corp.com"}
	cases := []struct {
		name        string
		cfg         config.EmailConfig
		allow       []string
		allowAnyone bool
		wantErr     string
	}{
		{"allowlist without gate", config.EmailConfig{}, allow, false, "anyone can forge"},
		{"gate without authserv id", config.EmailConfig{RequireAuthenticationResults: true}, allow, false, "trusted_authserv_id is required"},
		{"gate with authserv id", config.EmailConfig{RequireAuthenticationResults: true, TrustedAuthservID: "mx.corp.com"}, allow, false, ""},
		{"explicit insecure opt-out", config.EmailConfig{InsecureTrustFrom: true}, allow, false, ""},
		{"no allowlist", config.EmailConfig{}, nil, false, ""},
		{"allow anyone", config.EmailConfig{}, allow, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEmailAuth(tc.cfg, tc.allow, tc.allowAnyone, silentLogger())
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestCheckEmailAuth(t *testing.T) {
	cases := []struct {
		cfg    config.EmailConfig
		status string
	}{
		{config.EmailConfig{}, ""},
		{config.EmailConfig{IMAPAddr: "i:1", RequireAuthenticationResults: true, TrustedAuthservID: "mx"}, "ok"},
		{config.EmailConfig{IMAPAddr: "i:1", RequireAuthenticationResults: true}, "fail"},
		{config.EmailConfig{IMAPAddr: "i:1", InsecureTrustFrom: true}, "warn"},
		{config.EmailConfig{IMAPAddr: "i:1", Allowlist: []string{"a@b"}}, "fail"},
		{config.EmailConfig{IMAPAddr: "i:1"}, "info"},
	}
	for _, tc := range cases {
		rows := checkEmailAuth(&config.Config{Email: tc.cfg})
		if tc.status == "" {
			assert.Empty(t, rows)
			continue
		}
		if assert.Len(t, rows, 1) {
			assert.Equal(t, tc.status, rows[0].Status, "%+v", tc.cfg)
		}
	}
}
