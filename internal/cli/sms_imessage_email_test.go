package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestSMSCmd_MissingProviderErrors(t *testing.T) {
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newSMSCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sms.provider")
}

func TestSMSCmd_HasFlags(t *testing.T) {
	cmd := newSMSCmd(&Options{Config: &config.Config{}})
	assert.NotNil(t, cmd.Flags().Lookup("provider"))
	assert.NotNil(t, cmd.Flags().Lookup("from"))
	assert.NotNil(t, cmd.Flags().Lookup("account-sid"))
	assert.NotNil(t, cmd.Flags().Lookup("auth-token"))
	assert.NotNil(t, cmd.Flags().Lookup("api-key"))
}

func TestIMessageCmd_MissingConfigErrors(t *testing.T) {
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newIMessageCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "imessage")
}

func TestIMessageCmd_HasFlags(t *testing.T) {
	cmd := newIMessageCmd(&Options{Config: &config.Config{}})
	assert.NotNil(t, cmd.Flags().Lookup("base-url"))
	assert.NotNil(t, cmd.Flags().Lookup("password"))
	assert.NotNil(t, cmd.Flags().Lookup("poll-interval"))
}

func TestEmailCmd_MissingIMAPErrors(t *testing.T) {
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newEmailCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "IMAP")
}

func TestEmailCmd_MissingSMTPErrors(t *testing.T) {
	opts := &Options{Config: &config.Config{Email: config.EmailConfig{
		IMAPAddr: "i:993", IMAPUsername: "u", IMAPPassword: "p",
	}}, Logger: silentLogger()}
	cmd := newEmailCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SMTP")
}

func TestEmailCmd_MissingFromErrors(t *testing.T) {
	opts := &Options{Config: &config.Config{Email: config.EmailConfig{
		IMAPAddr: "i:993", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "s:587", SMTPUsername: "u", SMTPPassword: "p",
	}}, Logger: silentLogger()}
	cmd := newEmailCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "from")
}

func TestEmailCmd_HasFlags(t *testing.T) {
	cmd := newEmailCmd(&Options{Config: &config.Config{}})
	for _, f := range []string{
		"imap-addr", "imap-username", "imap-password",
		"smtp-addr", "smtp-username", "smtp-password",
		"from", "mailbox", "poll-interval",
	} {
		assert.NotNil(t, cmd.Flags().Lookup(f), "missing flag %s", f)
	}
}
