// Package stripe registers read-only Stripe tools (list_charges,
// get_customer) into the tool registry. Deliberately read-only —
// the operator's shell should never be able to move money.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// DefaultBaseURL is the Stripe API base.
const DefaultBaseURL = "https://api.stripe.com/v1"

// EnvToken is the environment variable read as a fallback for
// Config.SecretKey. Should be a restricted (sk_live_..._restricted)
// key with read-only permissions.
const EnvToken = "ROUSSEAU_STRIPE_KEY"

// Config configures the Stripe client.
type Config struct {
	// SecretKey is a Stripe secret key. Restricted read-only keys are
	// strongly recommended.
	SecretKey string
	// BaseURL overrides DefaultBaseURL — set for tests.
	BaseURL string
	// HTTPClient is injected in tests.
	HTTPClient *http.Client
}

// Client is a thin Stripe REST client scoped to the read operations
// the registered tools need.
type Client struct {
	key     string
	baseURL string
	http    *http.Client
}

// New constructs a Client.
func New(cfg Config) (*Client, error) {
	key := cfg.SecretKey
	if key == "" {
		key = os.Getenv(EnvToken)
	}
	if key == "" {
		return nil, fmt.Errorf("stripe: SecretKey required (set $%s or Config.SecretKey)", EnvToken)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	h := cfg.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{key: key, baseURL: base, http: h}, nil
}

// get issues a GET with Basic auth (Stripe uses the key as the
// username, no password).
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	full := c.baseURL + path
	if enc := q.Encode(); enc != "" {
		full += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return fmt.Errorf("stripe: build request: %w", err)
	}
	req.SetBasicAuth(c.key, "")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("stripe: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024)) //nolint:errcheck // best-effort read of error body
		return fmt.Errorf("stripe: %s: HTTP %d: %s", path, resp.StatusCode, string(snippet))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("stripe: decode: %w", err)
		}
	}
	return nil
}

// -- list_charges ----------------------------------------------------

// ListChargesTool lists recent charges.
type ListChargesTool struct{ c *Client }

// NewListChargesTool constructs a ListChargesTool.
func NewListChargesTool(c *Client) *ListChargesTool { return &ListChargesTool{c: c} }

// Name implements tools.Tool.
func (*ListChargesTool) Name() string { return "stripe_list_charges" }

// Description implements tools.Tool.
func (*ListChargesTool) Description() string {
	return "List recent Stripe charges (read-only). Optional: customer, limit (default 10, max 100)."
}

// InputSchema implements tools.Tool.
func (*ListChargesTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"customer": map[string]any{"type": "string"},
			"limit":    map[string]any{"type": "integer"},
		},
	}
}

// Execute implements tools.Tool.
func (t *ListChargesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Customer string `json:"customer"`
		Limit    int    `json:"limit"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if args.Limit == 0 {
		args.Limit = 10
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", args.Limit))
	if args.Customer != "" {
		q.Set("customer", args.Customer)
	}
	var out any
	if err := t.c.get(ctx, "/charges", q, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// -- get_customer ----------------------------------------------------

// GetCustomerTool fetches a customer's metadata.
type GetCustomerTool struct{ c *Client }

// NewGetCustomerTool constructs a GetCustomerTool.
func NewGetCustomerTool(c *Client) *GetCustomerTool { return &GetCustomerTool{c: c} }

// Name implements tools.Tool.
func (*GetCustomerTool) Name() string { return "stripe_get_customer" }

// Description implements tools.Tool.
func (*GetCustomerTool) Description() string {
	return "Fetch a Stripe customer by id (starts 'cus_')."
}

// InputSchema implements tools.Tool.
func (*GetCustomerTool) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"required":   []string{"id"},
	}
}

// Execute implements tools.Tool.
func (t *GetCustomerTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct{ ID string }
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.ID == "" {
		return "", fmt.Errorf("id is required")
	}
	var out any
	if err := t.c.get(ctx, "/customers/"+url.PathEscape(args.ID), nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// Register wires every Stripe tool into reg.
func Register(reg *tools.Registry, c *Client) error {
	for _, t := range []tools.Tool{
		NewListChargesTool(c),
		NewGetCustomerTool(c),
	} {
		if err := reg.Register(t); err != nil {
			return fmt.Errorf("stripe: register %s: %w", t.Name(), err)
		}
	}
	return nil
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(b), nil
}
