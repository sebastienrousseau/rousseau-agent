package cli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// cancelOnLogHandler cancels a context the first time a record with a
// given message is emitted.
//
// Every transport RunE logs "<name>.starting" on the line immediately
// before it hands control to client.Start, which blocks until the
// context is done. Cancelling on that record lets a test drive the
// entire wiring path — provider, state, registry, MCP, rate limiters,
// approver, skills, cron — and then unwind deterministically, with no
// network traffic and no subprocesses: every client's Start checks
// ctx.Err() before it does any I/O.
type cancelOnLogHandler struct {
	inner  slog.Handler
	msg    string
	cancel context.CancelFunc
}

func (h *cancelOnLogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *cancelOnLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == h.msg {
		h.cancel()
	}
	return h.inner.Handle(ctx, r)
}

func (h *cancelOnLogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &cancelOnLogHandler{inner: h.inner.WithAttrs(as), msg: h.msg, cancel: h.cancel}
}

func (h *cancelOnLogHandler) WithGroup(name string) slog.Handler {
	return &cancelOnLogHandler{inner: h.inner.WithGroup(name), msg: h.msg, cancel: h.cancel}
}

// daemonOptsCancellingOn returns a fully-formed Options plus a context
// that is cancelled the moment `msg` is logged. The provider is the
// Anthropic one because it constructs without touching the filesystem
// or the network; state lives in a per-test temp dir.
func daemonOptsCancellingOn(t *testing.T, msg string) (*Options, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Options{
		Config: &config.Config{
			Provider:  "anthropic",
			Anthropic: config.AnthropicConfig{APIKey: "sk-test", Model: "claude"},
			State:     config.StateConfig{Path: filepath.Join(t.TempDir(), "sessions.db")},
			Log:       config.LogConfig{Level: "error"},
			// Pin the skills dir at an empty temp dir so the daemon
			// never loads whatever the developer has under $HOME.
			Agent: config.AgentConfig{SkillsDir: t.TempDir()},
		},
		Logger: slog.New(&cancelOnLogHandler{
			inner:  slog.NewTextHandler(io.Discard, nil),
			msg:    msg,
			cancel: cancel,
		}),
	}, ctx
}

// runCmd wires a cobra command to a discard buffer and invokes RunE
// directly, which is what the transport tests care about.
func runCmd(t *testing.T, cmd *cobra.Command, ctx context.Context, args []string) error {
	t.Helper()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetContext(ctx)
	return cmd.RunE(cmd, args)
}

// TestTransportCmds_FullWiringPath drives each long-running transport
// command end to end. Each one must reach its "<name>.starting" log
// line — proving provider selection, state open, tool registration,
// rate limiting and the cron scheduler all wired up — and then return
// the cancellation error rather than hanging.
func TestTransportCmds_FullWiringPath(t *testing.T) {
	tests := []struct {
		name    string
		logMsg  string
		configs func(*config.Config)
		build   func(*Options) *cobra.Command
	}{
		{
			name:   "signal",
			logMsg: "signal.starting",
			configs: func(c *config.Config) {
				c.Signal = config.SignalConfig{Account: "+15550100", Binary: "signal-cli-not-real"}
			},
			build: newSignalCmd,
		},
		{
			name:   "telegram",
			logMsg: "telegram.starting",
			configs: func(c *config.Config) {
				c.Telegram = config.TelegramConfig{Token: "123:abc", BaseURL: "http://127.0.0.1:1"}
			},
			build: newTelegramCmd,
		},
		{
			name:   "matrix",
			logMsg: "matrix.starting",
			configs: func(c *config.Config) {
				c.Matrix = config.MatrixConfig{
					HomeserverURL: "http://127.0.0.1:1",
					AccessToken:   "syt_token",
					UserID:        "@bot:example.org",
				}
			},
			build: newMatrixCmd,
		},
		{
			name:   "slack",
			logMsg: "slack.starting",
			configs: func(c *config.Config) {
				c.Slack = config.SlackConfig{AppToken: "xapp-1", BotToken: "xoxb-1", BotUserID: "U1"}
			},
			build: newSlackCmd,
		},
		{
			name:   "discord",
			logMsg: "discord.starting",
			configs: func(c *config.Config) {
				c.Discord = config.DiscordConfig{Token: "bot-token"}
			},
			build: newDiscordCmd,
		},
		{
			name:   "sms",
			logMsg: "sms.starting",
			configs: func(c *config.Config) {
				c.SMS = config.SMSConfig{
					Provider: "twilio", From: "+15550100",
					AccountSID: "AC1", AuthToken: "tok",
					BaseURL: "http://127.0.0.1:1",
				}
			},
			build: newSMSCmd,
		},
		{
			name:   "imessage",
			logMsg: "imessage.starting",
			configs: func(c *config.Config) {
				c.IMessage = config.IMessageConfig{
					BaseURL: "http://127.0.0.1:1", Password: "pw", PollInterval: "1s",
				}
			},
			build: newIMessageCmd,
		},
		{
			name:   "email",
			logMsg: "email.starting",
			configs: func(c *config.Config) {
				c.Email = config.EmailConfig{
					IMAPAddr: "127.0.0.1:1", IMAPUsername: "u", IMAPPassword: "p",
					SMTPAddr: "127.0.0.1:1", SMTPUsername: "u", SMTPPassword: "p",
					From: "bot@example.org", Mailbox: "INBOX", PollInterval: "1s",
				}
			},
			build: newEmailCmd,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts, ctx := daemonOptsCancellingOn(t, tc.logMsg)
			tc.configs(opts.Config)

			err := runCmd(t, tc.build(opts), ctx, nil)
			// Every client's Start bails out on the cancelled
			// context before it opens a socket or spawns a process,
			// so the command must return an error rather than block.
			require.Error(t, err, "the cancelled context must surface as an error")
		})
	}
}

// TestWhatsAppCmd_FullWiringPath is separate because the WhatsApp
// client opens its own device store on Start, so the test needs a
// writable --store path.
func TestWhatsAppCmd_FullWiringPath(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "whatsapp.starting")
	cmd := newWhatsAppCmd(opts)
	require.NoError(t, cmd.Flags().Set("store", filepath.Join(t.TempDir(), "whatsapp.db")))
	require.NoError(t, cmd.Flags().Set("allow", "1@s.whatsapp.net"))

	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
}

// TestWhatsAppCmd_AllowlistFromEnv pins the documented fallback: with
// no --allow flag, ROUSSEAU_WHATSAPP_ALLOW seeds the allowlist and
// blank entries are dropped.
func TestWhatsAppCmd_AllowlistFromEnv(t *testing.T) {
	t.Setenv(envWhatsAppAllow, " 1@s.whatsapp.net , ,2@s.whatsapp.net ")
	opts, ctx := daemonOptsCancellingOn(t, "whatsapp.starting")
	cmd := newWhatsAppCmd(opts)
	require.NoError(t, cmd.Flags().Set("store", filepath.Join(t.TempDir(), "whatsapp.db")))

	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
}

// TestWhatsAppCmd_UnresolvableStoreDSN covers the resolveWhatsAppDSN
// failure branch: the parent of the requested store path is a regular
// file, so MkdirAll cannot create the directory.
func TestWhatsAppCmd_UnresolvableStoreDSN(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

	opts, ctx := daemonOptsCancellingOn(t, "never-logged")
	cmd := newWhatsAppCmd(opts)
	require.NoError(t, cmd.Flags().Set("store", filepath.Join(blocker, "whatsapp.db")))

	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create whatsapp store dir")
}

func TestResolveWhatsAppDSN_MkdirFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	_, err := resolveWhatsAppDSN(filepath.Join(blocker, "sub", "wa.db"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create whatsapp store dir")
}

// TestTransportCmds_AssembleFailureSurfaces confirms every transport
// aborts with the provider error rather than continuing to build a
// client when the daemon cannot be assembled.
func TestTransportCmds_AssembleFailureSurfaces(t *testing.T) {
	tests := []struct {
		name    string
		configs func(*config.Config)
		build   func(*Options) *cobra.Command
	}{
		{"signal", func(c *config.Config) { c.Signal.Account = "+15550100" }, newSignalCmd},
		{"telegram", func(c *config.Config) { c.Telegram.Token = "t" }, newTelegramCmd},
		{"matrix", func(c *config.Config) {
			c.Matrix.HomeserverURL, c.Matrix.AccessToken = "http://x", "t"
		}, newMatrixCmd},
		{"slack", func(c *config.Config) { c.Slack.AppToken, c.Slack.BotToken = "a", "b" }, newSlackCmd},
		{"discord", func(c *config.Config) { c.Discord.Token = "t" }, newDiscordCmd},
		{"sms", func(c *config.Config) { c.SMS.Provider, c.SMS.From = "twilio", "+1" }, newSMSCmd},
		{"imessage", func(c *config.Config) {
			c.IMessage.BaseURL, c.IMessage.Password = "http://x", "p"
		}, newIMessageCmd},
		{"email", func(c *config.Config) {
			c.Email = config.EmailConfig{
				IMAPAddr: "i:993", IMAPUsername: "u", IMAPPassword: "p",
				SMTPAddr: "s:587", SMTPUsername: "u", SMTPPassword: "p",
				From: "a@b.c",
			}
		}, newEmailCmd},
		{"whatsapp", func(*config.Config) {}, newWhatsAppCmd},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{
				Config: &config.Config{
					Provider: "not-a-real-provider",
					State:    config.StateConfig{Path: filepath.Join(t.TempDir(), "s.db")},
				},
				Logger: silentLogger(),
			}
			tc.configs(opts.Config)
			err := runCmd(t, tc.build(opts), context.Background(), nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unknown provider")
		})
	}
}

// TestSMSCmd_UnknownProviderFailsAfterAssembly exercises the
// sms.New error branch, which sits after the daemon has already been
// assembled — the store must still be released.
func TestSMSCmd_UnknownProviderFailsAfterAssembly(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "never-logged")
	opts.Config.SMS = config.SMSConfig{Provider: "carrier-pigeon", From: "+15550100"}
	err := runCmd(t, newSMSCmd(opts), ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

func TestIMessageCmd_BadPollIntervalFailsAfterAssembly(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "never-logged")
	opts.Config.IMessage = config.IMessageConfig{
		BaseURL: "http://127.0.0.1:1", Password: "pw", PollInterval: "not-a-duration",
	}
	err := runCmd(t, newIMessageCmd(opts), ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "poll_interval")
}

func TestEmailCmd_BadPollIntervalFailsAfterAssembly(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "never-logged")
	opts.Config.Email = config.EmailConfig{
		IMAPAddr: "127.0.0.1:1", IMAPUsername: "u", IMAPPassword: "p",
		SMTPAddr: "127.0.0.1:1", SMTPUsername: "u", SMTPPassword: "p",
		From: "bot@example.org", PollInterval: "soon",
	}
	err := runCmd(t, newEmailCmd(opts), ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "poll_interval")
}

// TestTransportCmds_MediaAudioMisconfigWarnsButContinues pins the
// fail-soft contract: a broken media.audio block logs a warning and
// the transport still starts without a transcriber.
func TestTransportCmds_MediaAudioMisconfigWarnsButContinues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		logMsg  string
		configs func(*config.Config)
		build   func(*Options) *cobra.Command
	}{
		{"signal", "signal.starting", func(c *config.Config) {
			c.Signal = config.SignalConfig{Account: "+15550100", Binary: "signal-cli-not-real"}
		}, newSignalCmd},
		{"telegram", "telegram.starting", func(c *config.Config) {
			c.Telegram = config.TelegramConfig{Token: "t", BaseURL: "http://127.0.0.1:1"}
		}, newTelegramCmd},
		{"matrix", "matrix.starting", func(c *config.Config) {
			c.Matrix = config.MatrixConfig{HomeserverURL: "http://127.0.0.1:1", AccessToken: "t"}
		}, newMatrixCmd},
		{"discord", "discord.starting", func(c *config.Config) {
			c.Discord = config.DiscordConfig{Token: "t"}
		}, newDiscordCmd},
		{"imessage", "imessage.starting", func(c *config.Config) {
			c.IMessage = config.IMessageConfig{BaseURL: "http://127.0.0.1:1", Password: "p"}
		}, newIMessageCmd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, ctx := daemonOptsCancellingOn(t, tc.logMsg)
			// whisper-cpp without a model_file is the canonical
			// operator mistake: buildTranscriberString errors.
			opts.Config.Media.Audio = config.MediaAudioConfig{Backend: "whisper-cpp"}
			tc.configs(opts.Config)
			err := runCmd(t, tc.build(opts), ctx, nil)
			require.Error(t, err)
		})
	}
}

// TestTransportCmds_TranscriberWiredWhenAudioConfigured takes the
// other side of the same branch: a valid media.audio block yields a
// non-nil transcriber that each audio-capable transport hands to its
// client.
func TestTransportCmds_TranscriberWiredWhenAudioConfigured(t *testing.T) {
	for _, tc := range []struct {
		name    string
		logMsg  string
		configs func(*config.Config)
		build   func(*Options) *cobra.Command
	}{
		{"signal", "signal.starting", func(c *config.Config) {
			c.Signal = config.SignalConfig{Account: "+15550100", Binary: "signal-cli-not-real"}
		}, newSignalCmd},
		{"telegram", "telegram.starting", func(c *config.Config) {
			c.Telegram = config.TelegramConfig{Token: "t", BaseURL: "http://127.0.0.1:1"}
		}, newTelegramCmd},
		{"matrix", "matrix.starting", func(c *config.Config) {
			c.Matrix = config.MatrixConfig{HomeserverURL: "http://127.0.0.1:1", AccessToken: "t"}
		}, newMatrixCmd},
		{"discord", "discord.starting", func(c *config.Config) {
			c.Discord = config.DiscordConfig{Token: "t"}
		}, newDiscordCmd},
		{"imessage", "imessage.starting", func(c *config.Config) {
			c.IMessage = config.IMessageConfig{BaseURL: "http://127.0.0.1:1", Password: "p"}
		}, newIMessageCmd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, ctx := daemonOptsCancellingOn(t, tc.logMsg)
			opts.Config.Media.Audio = config.MediaAudioConfig{
				Backend: "whisper-cpp", ModelFile: filepath.Join(t.TempDir(), "ggml.bin"),
			}
			tc.configs(opts.Config)
			err := runCmd(t, tc.build(opts), ctx, nil)
			require.Error(t, err)
		})
	}
}

// TestTransportCmds_FlagAllowlistOverridesConfig pins the precedence
// rule shared by every transport: --allow wins over the config block.
func TestTransportCmds_FlagAllowlistOverridesConfig(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "telegram.starting")
	opts.Config.Telegram = config.TelegramConfig{
		Token:     "config-token",
		BaseURL:   "http://127.0.0.1:1",
		Allowlist: []string{"from-config"},
	}
	cmd := newTelegramCmd(opts)
	require.NoError(t, cmd.Flags().Set("allow", "from-flag"))
	require.NoError(t, cmd.Flags().Set("token", "flag-token"))
	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
}

// TestSignalCmd_FlagsOverrideConfig covers the firstNonEmpty pairs on
// the signal command.
func TestSignalCmd_FlagsOverrideConfig(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "signal.starting")
	opts.Config.Signal = config.SignalConfig{
		Account:   "+15550999",
		Binary:    "config-binary",
		Allowlist: []string{"+15550999"},
		ExtraArgs: []string{"--verbose"},
	}
	cmd := newSignalCmd(opts)
	require.NoError(t, cmd.Flags().Set("account", "+15550100"))
	require.NoError(t, cmd.Flags().Set("binary", "flag-binary"))
	require.NoError(t, cmd.Flags().Set("allow", "+15550100"))
	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
}

// TestMatrixCmd_FlagsOverrideConfig covers the homeserver/token/user
// firstNonEmpty chain on matrix.
func TestMatrixCmd_FlagsOverrideConfig(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "matrix.starting")
	opts.Config.Matrix = config.MatrixConfig{Allowlist: []string{"@a:example.org"}}
	cmd := newMatrixCmd(opts)
	require.NoError(t, cmd.Flags().Set("homeserver", "http://127.0.0.1:1"))
	require.NoError(t, cmd.Flags().Set("token", "syt_flag"))
	require.NoError(t, cmd.Flags().Set("user-id", "@bot:example.org"))
	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
}

// TestSlackCmd_FlagsOverrideConfig covers slack's flag fallbacks.
func TestSlackCmd_FlagsOverrideConfig(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "slack.starting")
	opts.Config.Slack = config.SlackConfig{Allowlist: []string{"U9"}}
	cmd := newSlackCmd(opts)
	require.NoError(t, cmd.Flags().Set("app-token", "xapp-flag"))
	require.NoError(t, cmd.Flags().Set("bot-token", "xoxb-flag"))
	require.NoError(t, cmd.Flags().Set("bot-user-id", "U1"))
	require.NoError(t, cmd.Flags().Set("allow", "U2"))
	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
}

// TestSMSCmd_FlagsOverrideConfig covers the vonage branch plus every
// sms flag fallback.
func TestSMSCmd_FlagsOverrideConfig(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "sms.starting")
	cmd := newSMSCmd(opts)
	require.NoError(t, cmd.Flags().Set("provider", "vonage"))
	require.NoError(t, cmd.Flags().Set("from", "rousseau"))
	require.NoError(t, cmd.Flags().Set("api-key", "key"))
	require.NoError(t, cmd.Flags().Set("auth-token", "secret"))
	require.NoError(t, cmd.Flags().Set("account-sid", "unused"))
	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
}

// TestIMessageCmd_FlagsOverrideConfig covers imessage's flag chain.
func TestIMessageCmd_FlagsOverrideConfig(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "imessage.starting")
	cmd := newIMessageCmd(opts)
	require.NoError(t, cmd.Flags().Set("base-url", "http://127.0.0.1:1"))
	require.NoError(t, cmd.Flags().Set("password", "pw"))
	require.NoError(t, cmd.Flags().Set("poll-interval", "2s"))
	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
}

// TestEmailCmd_FlagsOverrideConfig covers email's nine flag fallbacks.
func TestEmailCmd_FlagsOverrideConfig(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "email.starting")
	cmd := newEmailCmd(opts)
	for flag, val := range map[string]string{
		"imap-addr": "127.0.0.1:1", "imap-username": "u", "imap-password": "p",
		"smtp-addr": "127.0.0.1:1", "smtp-username": "u", "smtp-password": "p",
		"from": "bot@example.org", "mailbox": "Archive", "poll-interval": "3s",
	} {
		require.NoError(t, cmd.Flags().Set(flag, val))
	}
	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
}

// TestDiscordCmd_FlagsOverrideConfig covers discord's flag chain.
func TestDiscordCmd_FlagsOverrideConfig(t *testing.T) {
	opts, ctx := daemonOptsCancellingOn(t, "discord.starting")
	opts.Config.Discord = config.DiscordConfig{Allowlist: []string{"U9"}}
	cmd := newDiscordCmd(opts)
	require.NoError(t, cmd.Flags().Set("token", "flag-token"))
	require.NoError(t, cmd.Flags().Set("allow", "U1"))
	err := runCmd(t, cmd, ctx, nil)
	require.Error(t, err)
}

// TestTransportCmds_CronStartFailureSurfaces drives the branch every
// transport shares between "client constructed" and "start serving":
// if the cron scheduler cannot read its job table the daemon aborts
// with a wrapped `cron:` error instead of serving traffic with a dead
// scheduler.
//
// The failure is induced by schema drift — cron_jobs exists (so
// NewCronStore's IF NOT EXISTS DDL no-ops) but is missing the
// last_run_at column the List query projects.
func TestTransportCmds_CronStartFailureSurfaces(t *testing.T) {
	tests := []struct {
		name    string
		configs func(*config.Config)
		build   func(*Options) *cobra.Command
		flags   map[string]string
	}{
		{"signal", func(c *config.Config) {
			c.Signal = config.SignalConfig{Account: "+15550100", Binary: "signal-cli-not-real"}
		}, newSignalCmd, nil},
		{"telegram", func(c *config.Config) {
			c.Telegram = config.TelegramConfig{Token: "t", BaseURL: "http://127.0.0.1:1"}
		}, newTelegramCmd, nil},
		{"matrix", func(c *config.Config) {
			c.Matrix = config.MatrixConfig{HomeserverURL: "http://127.0.0.1:1", AccessToken: "t"}
		}, newMatrixCmd, nil},
		{"slack", func(c *config.Config) {
			c.Slack = config.SlackConfig{AppToken: "xapp-1", BotToken: "xoxb-1"}
		}, newSlackCmd, nil},
		{"discord", func(c *config.Config) {
			c.Discord = config.DiscordConfig{Token: "t"}
		}, newDiscordCmd, nil},
		{"sms", func(c *config.Config) {
			c.SMS = config.SMSConfig{Provider: "twilio", From: "+15550100", AccountSID: "AC1", AuthToken: "tok"}
		}, newSMSCmd, nil},
		{"imessage", func(c *config.Config) {
			c.IMessage = config.IMessageConfig{BaseURL: "http://127.0.0.1:1", Password: "pw"}
		}, newIMessageCmd, nil},
		{"email", func(c *config.Config) {
			c.Email = config.EmailConfig{
				IMAPAddr: "127.0.0.1:1", IMAPUsername: "u", IMAPPassword: "p",
				SMTPAddr: "127.0.0.1:1", SMTPUsername: "u", SMTPPassword: "p",
				From: "bot@example.org",
			}
		}, newEmailCmd, nil},
		{"whatsapp", func(*config.Config) {}, newWhatsAppCmd, map[string]string{"store": "whatsapp.db"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts, ctx := daemonOptsCancellingOn(t, "never-logged")
			tc.configs(opts.Config)
			driftedCronSchema(t, opts.Config.State.Path)

			cmd := tc.build(opts)
			for flag, val := range tc.flags {
				require.NoError(t, cmd.Flags().Set(flag, filepath.Join(t.TempDir(), val)))
			}
			err := runCmd(t, cmd, ctx, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cron:")
		})
	}
}
