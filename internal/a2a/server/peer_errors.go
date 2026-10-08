package server

import (
	"errors"
	"log/slog"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// peerError logs err with a fresh reference and returns the generic
// message a peer sees in its place: summary plus that reference.
// Internal detail (addresses, paths, upstream and decoder messages)
// stays in the operator's logs and never crosses the A2A boundary.
func (s *Server) peerError(summary string, err error, attrs ...any) string {
	ref := a2a.ErrorRef()
	args := append([]any{slog.String("ref", ref), slog.String("err", err.Error())}, attrs...)
	s.logger().Warn("a2a.peer_error: "+summary, args...)
	return a2a.PeerErrorMessage(summary, ref)
}

// spawnErrText is the peer-facing text for a spawnTask error. The
// sentinel refusals (duplicate id, in-flight cap) are written for
// peers and pass through; anything else is reported generically.
func (s *Server) spawnErrText(err error) string {
	if errors.Is(err, errTaskExists) || errors.Is(err, errTooManyTasks) {
		return err.Error()
	}
	return s.peerError("task rejected", err)
}
