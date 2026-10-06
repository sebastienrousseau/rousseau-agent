// Package model holds the conversation domain types shared by every
// layer: messages and their content blocks, sessions, the Provider
// request/response contract and the streaming event shape.
//
// It depends on nothing inside the repository except internal/tools
// (for tool definitions on a Request), so a provider adapter or a
// store can be compiled, tested and reused without pulling in the
// agent loop and its orchestration dependencies (SSO, audit egress,
// progress, reliability). internal/agent re-exports every identifier
// here as a type alias, so existing call sites keep working; new code
// should import model directly.
package model
