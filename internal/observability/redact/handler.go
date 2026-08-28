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

// Handle scrubs every attribute in-place and forwards to inner.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	rewritten := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
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
// either the original attribute or a redacted replacement.
func (h *Handler) scrubAttr(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		nested := a.Value.Group()
		scrubbed := make([]any, 0, len(nested)*2)
		for _, na := range nested {
			s := h.scrubAttr(na)
			scrubbed = append(scrubbed, s.Key, s.Value)
		}
		return slog.Group(a.Key, scrubbed...)
	}

	raw := a.Value.String()
	for _, rule := range h.rules {
		if rule.KeyPattern != nil && rule.KeyPattern.MatchString(a.Key) {
			if rule.ValuePattern.MatchString(raw) {
				return slog.String(a.Key, marker(rule.Class))
			}
		}
		if rule.KeyPattern == nil && rule.ValuePattern.MatchString(raw) {
			return slog.String(a.Key, rule.ValuePattern.ReplaceAllString(raw, marker(rule.Class)))
		}
	}
	return a
}

// marker is the replacement text written in place of a matched value.
// Format is intentionally noisy so operators grepping logs can tell
// scrubbing happened.
func marker(c Class) string {
	return fmt.Sprintf("«redacted:%s»", c)
}
