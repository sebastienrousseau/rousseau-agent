// Package router implements a multi-model routing provider that
// delegates each request to a child [agent.Provider] chosen by
// matching request characteristics against a config-driven rule list.
//
// Typical use: send chit-chat to a cheap model (haiku), reasoning to a
// mid model (sonnet), and heavy-tool-use turns to the top-tier model
// (opus). Empirical savings on mixed workloads: 30-50% (SkillRouter
// literature) without measurable quality loss on the routed queries.
//
// # Config shape
//
//	provider: router
//	router:
//	  default: sonnet
//	  rules:
//	    - if: {message_len_max: 200, tool_use_count_max: 0}
//	      use: haiku
//	    - if: {tool_use_count_min: 3}
//	      use: opus
//	  providers:
//	    haiku:  {kind: anthropic, model: claude-haiku-4-5}
//	    sonnet: {kind: anthropic, model: claude-sonnet-4-6}
//	    opus:   {kind: anthropic, model: claude-opus-4-6}
//
// Rules are evaluated first-match-wins in the order declared. When no
// rule matches, the request goes to the default provider. A router
// with an empty rule list is a passthrough to the default (useful for
// bootstrapping — you can drop `router:` in front of an existing
// provider and add rules later).
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
)

// Rule is one decision node in the routing table.
type Rule struct {
	// Name is a human-readable identifier surfaced in metrics and logs.
	// Empty gets the string form of the child provider assigned.
	Name string
	// MessageLenMax matches when the last user message's total text
	// length (bytes across every text content block) is at most this
	// value. Zero means "no upper bound" (always matches).
	MessageLenMax int
	// MessageLenMin matches when the last user message's total text
	// length is at least this value. Zero means "no lower bound".
	MessageLenMin int
	// ToolUseCountMax matches when the number of tool_use messages
	// already in the session is at most this value. Zero disables
	// this filter (matches every request).
	ToolUseCountMax int
	// ToolUseCountMin matches when the number of tool_use messages is
	// at least this value.
	ToolUseCountMin int
	// SessionIDPrefix matches when Request.SessionID starts with this
	// string. Empty disables this filter. Useful for pinning a
	// specific test-tenant's traffic to a cheap model.
	SessionIDPrefix string
	// Use names the child provider to route to when this rule matches.
	// Must correspond to a key in [Router.providers].
	Use string
}

// Router is an [agent.Provider] that delegates to one of several
// child providers based on Rules evaluated against the incoming
// request.
type Router struct {
	defaultKey string
	rules      []Rule
	providers  map[string]agent.Provider
	logger     *slog.Logger
}

// Options collects the constructor arguments for [New].
type Options struct {
	// Default names the fallback provider used when no rule matches.
	// Must be a key in Providers. Required.
	Default string
	// Rules is the ordered list of routing rules — first match wins.
	// Empty list makes the router a passthrough to the default.
	Rules []Rule
	// Providers maps a rule "use" key to a concrete provider. Every
	// Rule.Use must be present here (validated in New).
	Providers map[string]agent.Provider
	// Logger is used for routing decisions logged at Debug. Nil uses
	// slog.Default.
	Logger *slog.Logger
}

// New constructs a Router. Returns an error when Default is empty,
// Providers is empty, Default isn't in Providers, or any Rule.Use
// references an unknown provider.
func New(opts Options) (*Router, error) {
	if opts.Default == "" {
		return nil, errors.New("router: Options.Default is required")
	}
	if len(opts.Providers) == 0 {
		return nil, errors.New("router: Options.Providers must contain at least the default")
	}
	if _, ok := opts.Providers[opts.Default]; !ok {
		return nil, fmt.Errorf("router: default provider %q not present in Providers", opts.Default)
	}
	for i, r := range opts.Rules {
		if r.Use == "" {
			return nil, fmt.Errorf("router: rule[%d] has empty Use", i)
		}
		if _, ok := opts.Providers[r.Use]; !ok {
			return nil, fmt.Errorf("router: rule[%d] Use=%q not present in Providers", i, r.Use)
		}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Router{
		defaultKey: opts.Default,
		rules:      opts.Rules,
		providers:  opts.Providers,
		logger:     logger,
	}, nil
}

// Name satisfies [agent.Provider]. Constant identifier so metrics keep
// a single label value regardless of which child fires.
func (*Router) Name() string { return "router" }

// Complete evaluates every rule in order against req; the first match
// wins. When no rule matches, the request goes to the default
// provider. Emits an observability.RouterDecisions counter with the
// rule name and chosen provider.
func (r *Router) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	key, ruleName := r.selectChild(req)
	provider := r.providers[key]
	observability.RouterDecisions.WithLabelValues(ruleName, key, provider.Name()).Inc()
	r.logger.Debug("router.decision",
		slog.String("rule", ruleName),
		slog.String("chosen_key", key),
		slog.String("chosen_provider", provider.Name()),
		slog.Int("last_user_len", lastUserTextLen(req.Messages)),
		slog.Int("tool_use_count", toolUseCount(req.Messages)),
	)
	return provider.Complete(ctx, req)
}

// selectChild returns (providerKey, ruleName) for the first matching
// rule, or (defaultKey, "default") when nothing matches.
func (r *Router) selectChild(req agent.Request) (string, string) {
	lastLen := lastUserTextLen(req.Messages)
	toolCount := toolUseCount(req.Messages)
	for _, rule := range r.rules {
		if !ruleMatches(rule, req, lastLen, toolCount) {
			continue
		}
		name := rule.Name
		if name == "" {
			name = rule.Use
		}
		return rule.Use, name
	}
	return r.defaultKey, "default"
}

// ruleMatches returns true when every non-zero constraint on rule is
// satisfied by the request. All conditions are AND'd.
func ruleMatches(rule Rule, req agent.Request, lastLen, toolCount int) bool {
	if rule.MessageLenMax > 0 && lastLen > rule.MessageLenMax {
		return false
	}
	if rule.MessageLenMin > 0 && lastLen < rule.MessageLenMin {
		return false
	}
	if rule.ToolUseCountMax > 0 && toolCount > rule.ToolUseCountMax {
		return false
	}
	if rule.ToolUseCountMin > 0 && toolCount < rule.ToolUseCountMin {
		return false
	}
	if rule.SessionIDPrefix != "" && !hasPrefix(req.SessionID, rule.SessionIDPrefix) {
		return false
	}
	return true
}

// -- helpers ---------------------------------------------------------

func lastUserTextLen(msgs []agent.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != agent.RoleUser {
			continue
		}
		total := 0
		for _, c := range msgs[i].Content {
			if c.Kind == agent.ContentText {
				total += len(c.Text)
			}
		}
		return total
	}
	return 0
}

func toolUseCount(msgs []agent.Message) int {
	n := 0
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Kind == agent.ContentToolUse {
				n++
			}
		}
	}
	return n
}

func hasPrefix(s, prefix string) bool {
	if len(prefix) == 0 {
		return true
	}
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}
