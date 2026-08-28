// Package redact provides a [log/slog] handler that scrubs credentials
// and PII out of every record before it reaches the underlying sink.
//
// The default rule set covers the credential patterns rousseau-agent
// touches (Anthropic/OpenAI/Slack/GitHub/AWS/JWT), plus a key-name
// pass that catches "token" / "secret" / "password" fields regardless
// of value shape. E.164 phone-number redaction is opt-in.
package redact

import (
	"regexp"
)

// Class labels the kind of secret a Rule matches. The redacted output
// carries the class so operators can tell what was scrubbed without
// leaking the value.
type Class string

// Well-known [Class] values.
const (
	// ClassAnthropic marks an Anthropic API key.
	ClassAnthropic Class = "anthropic"
	// ClassOpenAI marks an OpenAI (or OpenAI-shaped) API key.
	ClassOpenAI Class = "openai"
	// ClassSlack marks a Slack bot / app token.
	ClassSlack Class = "slack"
	// ClassGitHub marks a GitHub PAT (classic or fine-grained).
	ClassGitHub Class = "github"
	// ClassAWS marks an AWS access key.
	ClassAWS Class = "aws"
	// ClassJWT marks a JSON Web Token.
	ClassJWT Class = "jwt"
	// ClassKey marks a value redacted because of its attribute key
	// name (e.g. "password", "authorization").
	ClassKey Class = "key"
	// ClassPhone marks an E.164 phone number.
	ClassPhone Class = "phone"
	// ClassGeneric is a fallback used by callers that supply custom
	// rules without a specific classification.
	ClassGeneric Class = "redacted"
)

// Rule redacts a value when [ValuePattern] matches. When
// [KeyPattern] is non-nil the rule also fires on that key regardless
// of the value shape (useful for keys like "authorization" whose
// contents don't otherwise look like secrets).
type Rule struct {
	// Name is a human-readable identifier used in logs and tests.
	Name string
	// Class is the label written into the replacement string.
	Class Class
	// KeyPattern matches record keys. Empty means "no key match — only
	// use ValuePattern".
	KeyPattern *regexp.Regexp
	// ValuePattern matches record values. Required.
	ValuePattern *regexp.Regexp
}

// DefaultRules returns the shipped rule set. Phone-number redaction is
// excluded — callers who want it should append [PhoneRule].
func DefaultRules() []Rule {
	return []Rule{
		{
			Name:         "anthropic-api-key",
			Class:        ClassAnthropic,
			ValuePattern: regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{80,}`),
		},
		{
			Name:         "openai-api-key",
			Class:        ClassOpenAI,
			ValuePattern: regexp.MustCompile(`sk-[A-Za-z0-9]{40,}`),
		},
		{
			Name:         "slack-bot-token",
			Class:        ClassSlack,
			ValuePattern: regexp.MustCompile(`xoxb-\d+-\d+-[A-Za-z0-9]+`),
		},
		{
			Name:         "slack-app-token",
			Class:        ClassSlack,
			ValuePattern: regexp.MustCompile(`xapp-1-[A-Za-z0-9-]+`),
		},
		{
			Name:         "github-pat-classic",
			Class:        ClassGitHub,
			ValuePattern: regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),
		},
		{
			Name:         "github-pat-fine-grained",
			Class:        ClassGitHub,
			ValuePattern: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{80,}`),
		},
		{
			Name:         "aws-access-key",
			Class:        ClassAWS,
			ValuePattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		},
		{
			Name:         "jwt",
			Class:        ClassJWT,
			ValuePattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
		},
		{
			Name:         "secret-like-key",
			Class:        ClassKey,
			KeyPattern:   regexp.MustCompile(`(?i)(token|secret|api[_-]?key|password|apikey|authorization|cookie|session|refresh)`),
			ValuePattern: regexp.MustCompile(`.{4,}`), // any non-trivial value
		},
	}
}

// PhoneRule redacts values shaped like a leading-plus E.164 number
// (7–15 digits). Opt-in because legitimate identifiers on some
// transports (Signal source ids) also look like phone numbers.
func PhoneRule() Rule {
	return Rule{
		Name:         "phone-e164",
		Class:        ClassPhone,
		ValuePattern: regexp.MustCompile(`\+?[1-9]\d{6,14}`),
	}
}
