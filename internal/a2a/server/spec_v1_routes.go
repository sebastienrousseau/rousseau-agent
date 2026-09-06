package server

// This file adds A2A v1.0.1-conformant routes alongside the legacy
// v0-shorthand routes served by the same [Server]. Wiring lives in
// [Server.mux] (see server.go); the handlers and shape converters
// live here so the legacy path stays visually separate from the spec
// path during the deprecation window (roadmap: v0.0.7 removes
// legacy).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// handleGetTaskVerb dispatches `GET /tasks/{spec}` — the {spec}
// wildcard is either a bare task id (legacy shape → handleStatus) or
// an id-with-verb like `abc123:subscribe` (spec shape → handleSpecSubscribe).
// See the mux() doc comment for why this dispatcher exists rather
// than separate registrations.
func (s *Server) handleGetTaskVerb(w http.ResponseWriter, r *http.Request) {
	spec := r.PathValue("spec")
	id, verb, hasVerb := strings.Cut(spec, ":")
	if !hasVerb {
		// Bare id — legacy GET /tasks/{id}
		s.handleStatusFor(w, id)
		return
	}
	switch verb {
	case "subscribe":
		s.handleSpecSubscribeFor(w, r, id)
	default:
		writeSpecErr(w, http.StatusNotFound, "unknown_verb", "unsupported task verb: "+verb)
	}
}

// handlePostTaskVerb dispatches `POST /tasks/{spec}`. The only defined
// spec verb here is `:cancel`; anything else is 404. Bare ids are 405
// (the legacy `POST /tasks` create route is a separate registration).
func (s *Server) handlePostTaskVerb(w http.ResponseWriter, r *http.Request) {
	spec := r.PathValue("spec")
	id, verb, hasVerb := strings.Cut(spec, ":")
	if !hasVerb {
		w.Header().Set("Allow", "GET")
		writeSpecErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST /tasks/{id} is not defined; use POST /tasks/{id}:cancel or POST /message:send")
		return
	}
	switch verb {
	case "cancel":
		s.handleSpecCancelFor(w, id)
	default:
		writeSpecErr(w, http.StatusNotFound, "unknown_verb", "unsupported task verb: "+verb)
	}
}

// handleStatusFor is the legacy status handler split so the verb
// dispatcher can call it without a fake *http.Request.
func (s *Server) handleStatusFor(w http.ResponseWriter, id string) {
	state := s.lookup(id)
	if state == nil {
		writeErr(w, http.StatusNotFound, "unknown task_id")
		return
	}
	writeJSON(w, http.StatusOK, state.snapshot())
}

// handleSpecCancelFor is the id-parameterised body of handleSpecCancel.
func (s *Server) handleSpecCancelFor(w http.ResponseWriter, id string) {
	state := s.lookup(id)
	if state == nil {
		writeSpecErr(w, http.StatusNotFound, "task_not_found", "unknown task id")
		return
	}
	state.cancel()
	writeSpecJSON(w, http.StatusAccepted, a2a.SpecTask{
		ID: id,
		Status: a2a.TaskStatus1{
			State:     a2a.TaskStateCanceled,
			Timestamp: time.Now().UTC(),
		},
	})
}

// handleSpecSubscribeFor is the id-parameterised body of handleSpecSubscribe.
func (s *Server) handleSpecSubscribeFor(w http.ResponseWriter, r *http.Request, id string) {
	state := s.lookup(id)
	if state == nil {
		writeSpecErr(w, http.StatusNotFound, "task_not_found", "unknown task id")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeSpecErr(w, http.StatusInternalServerError, "streaming_unsupported", "SSE not supported by this ResponseWriter")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("A2A-Version", a2a.SpecVersion)

	ch, cancel := state.subscribe()
	defer cancel()

	for _, upd := range state.history() {
		if err := writeSpecSSE(w, id, upd); err != nil {
			return
		}
		flusher.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case upd, alive := <-ch:
			if !alive {
				return
			}
			if err := writeSpecSSE(w, id, upd); err != nil {
				return
			}
			flusher.Flush()
			if isTerminal(upd.Status) {
				return
			}
		}
	}
}

// handleSpecCard serves the A2A v1.0 well-known agent card at
// `/.well-known/agent-card.json`. It transforms the operator's
// [a2a.CapabilityCard] into the spec-shaped [a2a.AgentCard], keeping
// the two card types in sync at request time so operators only need to
// configure one.
func (s *Server) handleSpecCard(w http.ResponseWriter, r *http.Request) {
	card := a2a.UpgradeCard(s.Card)
	// Advertise the interface we actually serve so peers know where
	// to send their spec-shaped requests.
	if card.URL == "" {
		card.URL = specBaseURL(r)
	}
	card.Interfaces = []a2a.AgentInterface{
		{URL: card.URL, ProtocolBinding: "REST", ProtocolVersion: a2a.SpecVersion},
	}
	card.PreferredTransport = "REST"
	writeSpecJSON(w, http.StatusOK, card)
}

// handleSpecMessageSend is the v1.0 `POST /message:send` handler. The
// body is a spec-shaped [a2a.Message]; the response is a spec-shaped
// [a2a.SpecTask]. Internally the message is flattened onto the
// legacy [a2a.Task] shape so the existing [Handler] pipeline keeps
// working during the deprecation window.
func (s *Server) handleSpecMessageSend(w http.ResponseWriter, r *http.Request) {
	var msg a2a.Message
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&msg); err != nil {
		writeSpecErr(w, http.StatusBadRequest, "invalid_message", "invalid message body: "+err.Error())
		return
	}
	prompt := a2a.PromptFromMessage(msg)
	if prompt == "" {
		writeSpecErr(w, http.StatusBadRequest, "empty_message", "message.parts[] must contain at least one non-empty text part")
		return
	}
	legacyTask := a2a.Task{
		TaskID:    msg.TaskID,
		FromAgent: msg.ContextID, // no direct equivalent in legacy; contextId carries the intent
		Prompt:    prompt,
	}
	if legacyTask.TaskID == "" {
		legacyTask.TaskID = newTaskID()
	}

	state := s.spawnTask(legacyTask)
	resp := a2a.SpecTask{
		ID:        state.id,
		ContextID: msg.ContextID,
		Status: a2a.TaskStatus1{
			State:     a2a.TaskStateSubmitted,
			Timestamp: time.Now().UTC(),
		},
	}
	writeSpecJSON(w, http.StatusAccepted, resp)
}

// writeSpecJSON is the v1 sibling of [writeJSON] — same behaviour but
// emits the spec-preferred Content-Type and sets the version header.
func writeSpecJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", a2a.ContentTypeSpec)
	w.Header().Set("A2A-Version", a2a.SpecVersion)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) //nolint:errcheck // client-closed conn is not our problem
}

// writeSpecErr emits a spec-shaped error body. The spec is flexible
// on error shape across transports; on REST we use a JSON body with
// `code` and `message` keys plus the HTTP status. The `code` field is
// a short machine-readable identifier, not a JSON-RPC integer — the
// integer form applies to the JSON-RPC transport, not REST.
func writeSpecErr(w http.ResponseWriter, status int, code, msg string) {
	writeSpecJSON(w, status, map[string]any{
		"code":    code,
		"message": msg,
	})
}

// writeSpecSSE emits one SSE frame carrying a [a2a.StreamResponse] with
// a statusUpdate payload derived from the legacy TaskUpdate.
func writeSpecSSE(w io.Writer, taskID string, upd a2a.TaskUpdate) error {
	event := a2a.StreamResponse{
		StatusUpdate: &a2a.TaskStatusUpdateEvent{
			TaskID: taskID,
			Kind:   "status-update",
			Status: a2a.TaskStatus1{
				State:     a2a.FromLegacyStatus(upd.Status),
				Timestamp: upd.At,
			},
		},
	}
	blob, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", blob)
	return err
}

// specBaseURL derives the base URL for the interface advertisement on
// the AgentCard from the inbound request. Uses X-Forwarded-Proto /
// X-Forwarded-Host when present so reverse-proxied deployments
// advertise the externally-visible URL, not the loopback the process
// is bound to.
func specBaseURL(r *http.Request) string {
	scheme := "http"
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}
