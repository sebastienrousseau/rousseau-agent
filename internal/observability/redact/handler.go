package redact

import (
	"context"
	"fmt"
	"log/slog"
)

// Handler wraps an underlying [slog.Handler] and rewrites every record
// attribute so credentials and PII never reach the sink. Rule
// evaluation is O(rules × attrs) — the default rule set is ten
// entries so the overhead is negligible under typical log volume.
//
// Zero-value Handler is not usable; construct via [New].
type Handler struct {
	inner slog.Handler
	rules []Rule
}

// New returns a Handler that wraps inner with the supplied rule set.
// Passing zero rules disables redaction — useful for the escape-hatch
// path where an operator sets ROUSSEAU_LOG_NO_REDACT=1 for debugging.
func New(inner slog.Handler, rules []Rule) *Handler {
	return &Handler{inner: inner, rules: rules}
}

// Enabled delegates to the inner handler unchanged. Redaction never
// affects level filtering.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle scrubs the message and every attribute, then forwards to inner.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	rewritten := slog.NewRecord(r.Time, r.Level, h.scrubString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		rewritten.AddAttrs(h.scrubAttr(a))
		return true
	})
	return h.inner.Handle(ctx, rewritten)
}

// WithAttrs wraps the returned handler so pre-bound attributes get
// scrubbed the same way as record-time attributes.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		scrubbed[i] = h.scrubAttr(a)
	}
	return &Handler{inner: h.inner.WithAttrs(scrubbed), rules: h.rules}
}

// WithGroup delegates to the inner handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), rules: h.rules}
}

// scrubAttr runs every rule against a single attribute and returns
// either the original attribute or a redacted replacement. LogValuer
// values are resolved first so the rules see what the sink would
// write, not the unresolved Go value.
func (h *Handler) scrubAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		nested := a.Value.Group()
		scrubbed := make([]any, 0, len(nested))
		for _, na := range nested {
			scrubbed = append(scrubbed, h.scrubAttr(na))
		}
		return slog.Group(a.Key, scrubbed...)
	}

	raw := a.Value.String()
	if h.keyMatches(a.Key, raw) {
		return slog.String(a.Key, marker(ClassKey))
	}
	if out := h.scrubString(raw); out != raw {
		return slog.String(a.Key, out)
	}
	return a
}

// keyMatches reports whether a key rule fires on the attribute. A key
// rule replaces the whole value, so it is checked before value rules.
func (h *Handler) keyMatches(key, raw string) bool {
	for _, rule := range h.rules {
		if rule.KeyPattern != nil && rule.KeyPattern.MatchString(key) && rule.ValuePattern.MatchString(raw) {
			return true
		}
	}
	return false
}

// scrubString applies every value-only rule in sequence, so a string
// holding several secrets loses all of them.
func (h *Handler) scrubString(s string) string {
	return applyValueRules(h.rules, s)
}

func applyValueRules(rules []Rule, s string) string {
	for _, rule := range rules {
		if rule.KeyPattern != nil || !rule.ValuePattern.MatchString(s) {
			continue
		}
		repl := rule.Replacement
		if repl == "" {
			repl = marker(rule.Class)
		}
		s = rule.ValuePattern.ReplaceAllString(s, repl)
	}
	return s
}

// defaultRules is compiled once for [String].
var defaultRules = DefaultRules()

// String scrubs s with the value rules of [DefaultRules]. Use it for
// text that leaves the process outside the logger, such as a chat
// message. Key rules do not apply: there is no key.
func String(s string) string {
	return applyValueRules(defaultRules, s)
}

// marker is the replacement text written in place of a matched value.
// Format is intentionally noisy so operators grepping logs can tell
// scrubbing happened.
func marker(c Class) string {
	return fmt.Sprintf("«redacted:%s»", c)
}
