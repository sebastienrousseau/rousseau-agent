package discord

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// transcribeAudio picks the first audio attachment on the message,
// downloads it, and hands it to Config.Transcriber. Returns "" on
// any failure — the caller drops the message rather than replying
// with a stub. Nil-Transcriber → "" without touching the network.
func (c *Client) transcribeAudio(ctx context.Context, m *discordMessage) string {
	if c.cfg.Transcriber == nil || len(m.Attachments) == 0 {
		return ""
	}
	var att *discordAttachment
	for i := range m.Attachments {
		ct := m.Attachments[i].ContentType
		if len(ct) >= 6 && ct[:6] == "audio/" {
			att = &m.Attachments[i]
			break
		}
	}
	if att == nil {
		return ""
	}
	blob, err := c.downloadAttachment(ctx, att.URL)
	if err != nil {
		c.logger.Warn("discord.audio.download_failed",
			slog.String("url", att.URL),
			slog.String("err", err.Error()))
		return ""
	}
	transcript, err := c.cfg.Transcriber.Transcribe(ctx, blob, att.ContentType)
	if err != nil {
		c.logger.Warn("discord.audio.transcribe_failed",
			slog.String("err", err.Error()))
		return ""
	}
	return transcript
}

// collectImageAttachments walks the message attachments, downloads
// each image, and applies MediaPolicy against the sniffed bytes.
// Failures per attachment are logged and dropped: one bad image must
// not swallow a whole message. Non-image envelope MIMEs
// short-circuit before any network I/O — Discord CDN egress is
// metered against the bot's rate limit.
func (c *Client) collectImageAttachments(ctx context.Context, atts []discordAttachment) []transport.Attachment {
	if len(atts) == 0 {
		return nil
	}
	out := make([]transport.Attachment, 0, len(atts))
	totalSoFar := 0
	for i := range atts {
		att := atts[i]
		if !isImageContentType(att.ContentType) {
			continue
		}
		if att.URL == "" {
			continue
		}
		data, err := c.downloadAttachment(ctx, att.URL)
		if err != nil {
			c.logger.Warn("discord.image.download_failed",
				slog.String("id", att.ID),
				slog.String("err", err.Error()))
			continue
		}
		sniffed, err := c.cfg.MediaPolicy.Accept(data, totalSoFar)
		if err != nil {
			c.logger.Info("discord.image.dropped",
				slog.String("id", att.ID),
				slog.String("reason", err.Error()),
				slog.Int("bytes", len(data)))
			continue
		}
		if sniffed != att.ContentType {
			c.logger.Warn("discord.image.mime_lied",
				slog.String("id", att.ID),
				slog.String("envelope", att.ContentType),
				slog.String("sniffed", sniffed))
		}
		out = append(out, transport.Attachment{MediaType: sniffed, Data: data})
		totalSoFar += len(data)
	}
	return out
}

// isImageContentType returns true when the envelope-reported MIME is
// image/*. Kept alongside the audio detection in transcribeAudio so
// both media branches share the same tiny prefix-check pattern.
func isImageContentType(ct string) bool {
	if len(ct) < 6 {
		return false
	}
	return ct[:6] == "image/"
}

// downloadAttachment GETs the attachment URL — Discord CDN URLs are
// signed and don't need the bot token. Capped at Config.MaxAudioBytes
// (or 32 MiB) via io.LimitReader.
func (c *Client) downloadAttachment(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discord attachment: HTTP %d", resp.StatusCode)
	}
	maxBytes := c.cfg.MaxAudioBytes
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
}
