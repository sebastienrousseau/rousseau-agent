// Package senderkey builds the key rousseau stores a sender under:
// "<transport>:<platform id>". Platform ids are not unique across
// transports (an E.164 number is a Signal and an iMessage handle; a
// numeric id is a Telegram chat and a Discord user), so state keyed on
// the bare id lets one transport's sender reach another's sessions.
package senderkey

import (
	"fmt"
	"regexp"
	"strings"
)

// Make returns the stored key for sender on transport. An empty
// transport (tests, the local chat CLI) or an empty sender returns
// sender unchanged.
func Make(transport, sender string) string {
	if transport == "" || sender == "" {
		return sender
	}
	return transport + ":" + sender
}

// Split returns the transport and bare sender of a key made by Make.
// ok is false for a key with no known transport prefix (a legacy bare
// sender), which is returned whole as sender.
func Split(key string) (transport, sender string, ok bool) {
	t, s, found := strings.Cut(key, ":")
	if !found || !Known(t) {
		return "", key, false
	}
	return t, s, true
}

// Bare strips a known transport prefix, for display.
func Bare(key string) string {
	_, s, _ := Split(key)
	return s
}

// transports are the names the daemon registers routers under.
var transports = map[string]bool{
	"whatsapp": true, "signal": true, "imessage": true, "email": true,
	"telegram": true, "discord": true, "slack": true, "matrix": true,
	"sms": true,
}

// Known reports whether t is a transport name keys are built from.
func Known(t string) bool { return transports[t] }

var (
	e164   = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
	digits = regexp.MustCompile(`^-?[0-9]+$`)
	email  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	slack  = regexp.MustCompile(`^[UW][A-Z0-9]{6,}$`)
	matrix = regexp.MustCompile(`^@[^:\s]+:[^\s]+$`)
)

// Infer guesses the transport of a legacy bare sender from its shape.
// candidates lists every transport that produces that shape; Infer
// returns ok only when there is exactly one.
func Infer(sender string) (transport string, candidates []string, ok bool) {
	switch {
	case strings.HasSuffix(sender, "@s.whatsapp.net"), strings.HasSuffix(sender, "@lid"),
		strings.HasSuffix(sender, "@g.us"):
		candidates = []string{"whatsapp"}
	case matrix.MatchString(sender):
		candidates = []string{"matrix"}
	case slack.MatchString(sender):
		candidates = []string{"slack"}
	case e164.MatchString(sender):
		candidates = []string{"imessage", "signal"}
	case email.MatchString(sender):
		candidates = []string{"email", "imessage"}
	case digits.MatchString(sender):
		candidates = []string{"discord", "telegram"}
	}
	if len(candidates) == 1 {
		return candidates[0], candidates, true
	}
	return "", candidates, false
}

// Change is one legacy key rewritten by a migration.
type Change struct {
	From, To string
	// Rule is "shape" (inferred from the key's form) or "map".
	Rule string
}

// Ambiguous is a legacy key whose transport cannot be inferred.
type Ambiguous struct {
	Sender     string
	Candidates []string
}

// Plan is how a migration rewrites a store's legacy keys.
type Plan struct {
	// Keys maps each legacy key to its namespaced form.
	Keys      map[string]string
	Changes   []Change
	Ambiguous []Ambiguous
}

// PlanKeys maps legacy bare keys to namespaced ones: by mapping when
// the operator gave one, else by shape. Keys already namespaced are
// left alone; keys whose shape fits several transports are reported
// as ambiguous rather than guessed. senders should be sorted for a
// stable report.
func PlanKeys(senders []string, mapping map[string]string) (Plan, error) {
	p := Plan{Keys: map[string]string{}}
	for _, k := range senders {
		if _, _, ok := Split(k); ok {
			continue
		}
		if t, ok := mapping[k]; ok {
			if !Known(t) {
				return p, fmt.Errorf("senderkey: --map %s=%s: unknown transport", k, t)
			}
			p.Keys[k] = Make(t, k)
			p.Changes = append(p.Changes, Change{From: k, To: p.Keys[k], Rule: "map"})
			continue
		}
		t, candidates, ok := Infer(k)
		if !ok {
			p.Ambiguous = append(p.Ambiguous, Ambiguous{Sender: k, Candidates: candidates})
			continue
		}
		p.Keys[k] = Make(t, k)
		p.Changes = append(p.Changes, Change{From: k, To: p.Keys[k], Rule: "shape"})
	}
	return p, nil
}
