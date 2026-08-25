package a2a_test

import (
	"net/http"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a/server"
)

// newTestMux returns the A2A router so httptest can wrap it without
// standing up a full listener.
func newTestMux(s *server.Server) http.Handler { return s.Router() }
