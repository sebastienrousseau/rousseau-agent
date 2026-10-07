package mcp

import (
	"encoding/json"
)

// toolsTTLMs is the cache hint for tools/list and server/discover: the
// tool set is fixed when the server starts, so clients may cache it
// for a while.
const toolsTTLMs = 5 * 60 * 1000

// SupportedProtocolVersions lists every revision the server speaks,
// newest first.
func SupportedProtocolVersions() []string {
	return append([]string{ModernProtocolVersion}, LegacyProtocolVersions...)
}

// requestMeta returns params._meta and whether it names a protocol
// version, which marks a 2026-07-28 stateless request. Initialize-era
// requests carry no version in _meta (at most a progressToken).
func requestMeta(params json.RawMessage) (map[string]json.RawMessage, bool) {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil {
		return nil, false
	}
	_, ok := p.Meta[MetaProtocolVersion]
	return p.Meta, ok
}

// checkStatelessMeta validates a stateless request's _meta: the version
// must be one the server speaks statelessly (-32022 with the supported
// list otherwise) and client capabilities must be declared (-32602).
// Notifications are never answered.
func checkStatelessMeta(env Envelope, meta map[string]json.RawMessage) *Envelope {
	if len(env.ID) == 0 {
		return nil
	}
	var version string
	_ = json.Unmarshal(meta[MetaProtocolVersion], &version) //nolint:errcheck // a non-string version is unsupported below
	if version != ModernProtocolVersion {
		resp := errorResponse(env.ID, CodeUnsupportedProtocolVersion, "unsupported protocol version: "+version)
		resp.Error.Data = map[string]any{"supported": SupportedProtocolVersions(), "requested": version}
		return resp
	}
	if _, ok := meta[MetaClientCapabilities]; !ok {
		return errorResponse(env.ID, CodeInvalidParams, "missing _meta "+MetaClientCapabilities)
	}
	return nil
}

// markComplete stamps a stateless result as complete (2026-07-28
// requires resultType on every result) and adds the cache hints
// tools/list must carry. Errors and notifications pass through.
func markComplete(resp *Envelope, method string) *Envelope {
	if resp == nil || resp.Error != nil {
		return resp
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(resp.Result, &obj) != nil || obj == nil {
		return resp
	}
	obj["resultType"] = json.RawMessage(`"complete"`)
	if method == MethodToolsList {
		obj["ttlMs"] = json.RawMessage(`300000`)
		obj["cacheScope"] = json.RawMessage(`"public"`)
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return errorResponse(resp.ID, CodeInternalError, "marshal result: "+err.Error())
	}
	resp.Result = b
	return resp
}

// handleDiscover answers server/discover, which every 2026-07-28
// server must implement: the revisions it speaks, its capabilities,
// and its identity in the result's _meta.
func (s *Server) handleDiscover(env Envelope) *Envelope {
	return okResponse(env.ID, map[string]any{
		"resultType":        "complete",
		"supportedVersions": SupportedProtocolVersions(),
		"capabilities":      ServerCapabilities{Tools: &ToolCapability{ListChanged: false}},
		"ttlMs":             toolsTTLMs,
		"cacheScope":        "public",
		"_meta":             map[string]any{"io.modelcontextprotocol/serverInfo": s.info},
	})
}
