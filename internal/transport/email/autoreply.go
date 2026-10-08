package email

import (
	"net/mail"
	"strings"
)

// autoReplyReason reports why a mail is automatic (an out-of-office,
// a bounce or list traffic) and must not start a turn, or "" when it
// looks human. Answering such mail is how two responders ping-pong
// (RFC 3834 §2).
func autoReplyReason(h mail.Header, from string) string {
	if local, _, _ := strings.Cut(from, "@"); local == "mailer-daemon" || local == "postmaster" {
		return "bounce sender"
	}
	if h == nil {
		return ""
	}
	if v := headerToken(h, "Auto-Submitted"); v != "" && v != "no" {
		return "Auto-Submitted header"
	}
	switch headerToken(h, "Precedence") {
	case "bulk", "list", "junk", "auto_reply":
		return "Precedence header"
	}
	if len(h["List-Id"]) > 0 || len(h["List-Unsubscribe"]) > 0 {
		return "mailing-list header"
	}
	return ""
}

// headerToken returns the lower-cased value of header key up to any
// ";" parameters, e.g. "auto-replied" for "Auto-Replied; x=y".
func headerToken(h mail.Header, key string) string {
	v, _, _ := strings.Cut(h.Get(key), ";")
	return strings.ToLower(strings.TrimSpace(v))
}
