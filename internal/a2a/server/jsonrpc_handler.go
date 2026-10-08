package server

// A2A JSON-RPC 2.0 binding — the second transport shape rousseau
// serves alongside the REST binding. See internal/a2a/jsonrpc.go for
// the wire types; docs/a2a-conformance.md for the deprecation
// timeline.
//
// One HTTP endpoint (POST /jsonrpc) accepts a JSONRPCRequest,
// dispatches on the Method field, and returns a JSONRPCResponse. The
// non-streaming methods dispatch to the same domain-logic paths the
// REST binding uses, so a peer talking JSON-RPC and a peer talking
// REST both hit identical downstream code.
//
// Streaming methods (SendStreamingMessage, SubscribeToTask) return
// SSE frames per the spec — the same StreamResponse shape the REST
// binding's :subscribe route emits.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// handleJSONRPC is the single POST /jsonrpc endpoint. Parses the
// envelope, validates, dispatches, and returns the response.
func (s *Server) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSONRPCErr(w, nil, a2a.JSONRPCErrParseError, s.peerError("read body", err))
		return
	}
	var req a2a.JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONRPCErr(w, nil, a2a.JSONRPCErrParseError, s.peerError("parse envelope", err))
		return
	}
	if err := req.Validate(); err != nil {
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrInvalidRequest, err.Error())
		return
	}

	switch req.Method {
	case a2a.MethodSendMessage:
		s.handleJSONRPCSendMessage(r.Context(), w, req)
	case a2a.MethodGetTask:
		s.handleJSONRPCGetTask(r.Context(), w, req)
	case a2a.MethodCancelTask:
		s.handleJSONRPCCancelTask(r.Context(), w, req)
	case a2a.MethodSendStreamingMessage, a2a.MethodSubscribeToTask:
		// Streaming methods share the SSE mechanics of the REST
		// binding's :subscribe route but wrap their frames in the
		// JSON-RPC response envelope. Not yet implemented — return
		// a spec-conformant "method not found" for now so peers
		// know to fall back to REST.
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrMethodNotFound,
			"streaming methods over JSON-RPC not yet supported; use REST /message:stream or /tasks/{id}:subscribe")
	default:
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrMethodNotFound, "unknown method: "+req.Method)
	}
}

// handleJSONRPCSendMessage dispatches a JSON-RPC SendMessage call to
// the same task-spawning path the REST /message:send route uses.
func (s *Server) handleJSONRPCSendMessage(ctx context.Context, w http.ResponseWriter, req a2a.JSONRPCRequest) {
	var msg a2a.Message
	if err := json.Unmarshal(req.Params, &msg); err != nil {
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrInvalidParams, s.peerError("params must be a Message", err))
		return
	}
	prompt := a2a.PromptFromMessage(msg)
	if prompt == "" {
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrInvalidParams, "message.parts[] must contain at least one non-empty text part")
		return
	}
	legacyTask := a2a.Task{
		TaskID:    msg.TaskID,
		FromAgent: msg.ContextID,
		Prompt:    prompt,
	}
	if legacyTask.TaskID == "" {
		legacyTask.TaskID = newTaskID()
	}
	state, err := s.spawnTask(ctx, legacyTask)
	if err != nil {
		writeJSONRPCErr(w, req.ID, spawnErrCode(err), s.spawnErrText(err))
		return
	}

	writeJSONRPCResult(w, req.ID, a2a.SpecTask{
		ID:        state.id,
		ContextID: msg.ContextID,
		Status: a2a.TaskStatus1{
			State:     a2a.TaskStateSubmitted,
			Timestamp: time.Now().UTC(),
		},
	})
}

// spawnErrCode maps a spawnTask error to its JSON-RPC error code. A
// reused task id is an invalid parameter; a full in-flight cap is
// -32029 (rate limited).
func spawnErrCode(err error) int {
	switch {
	case errors.Is(err, errTaskExists):
		return a2a.JSONRPCErrInvalidParams
	case errors.Is(err, errTooManyTasks):
		return a2a.JSONRPCErrRateLimited
	default:
		return a2a.JSONRPCErrInternal
	}
}

// jsonrpcTaskIDParams is the shape both GetTask and CancelTask expect.
type jsonrpcTaskIDParams struct {
	ID string `json:"id"`
}

// handleJSONRPCGetTask returns a snapshot of the referenced task.
func (s *Server) handleJSONRPCGetTask(ctx context.Context, w http.ResponseWriter, req a2a.JSONRPCRequest) {
	var params jsonrpcTaskIDParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrInvalidParams, s.peerError("params.id required", err))
		return
	}
	if params.ID == "" {
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrInvalidParams, "params.id required")
		return
	}
	state := s.lookupFor(ctx, params.ID)
	if state == nil {
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrTaskNotFound, "no such task: "+params.ID)
		return
	}
	snap := state.snapshot()
	writeJSONRPCResult(w, req.ID, a2a.SpecTask{
		ID:        snap.TaskID,
		ContextID: snap.Last.TaskID,
		Status: a2a.TaskStatus1{
			State:     a2a.FromLegacyStatus(snap.Status),
			Timestamp: snap.Last.At,
		},
	})
}

// handleJSONRPCCancelTask cancels the referenced task.
func (s *Server) handleJSONRPCCancelTask(ctx context.Context, w http.ResponseWriter, req a2a.JSONRPCRequest) {
	var params jsonrpcTaskIDParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrInvalidParams, s.peerError("params.id required", err))
		return
	}
	if params.ID == "" {
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrInvalidParams, "params.id required")
		return
	}
	state := s.lookupFor(ctx, params.ID)
	if state == nil {
		writeJSONRPCErr(w, req.ID, a2a.JSONRPCErrTaskNotFound, "no such task: "+params.ID)
		return
	}
	state.cancel()
	writeJSONRPCResult(w, req.ID, a2a.SpecTask{
		ID: params.ID,
		Status: a2a.TaskStatus1{
			State:     a2a.TaskStateCanceled,
			Timestamp: time.Now().UTC(),
		},
	})
}

// writeJSONRPCResult encodes a successful response. Always emits HTTP
// 200 per the JSON-RPC 2.0 spec — protocol-level errors ride the
// envelope's Error field, not the HTTP status.
func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	resp, err := a2a.NewResultResponse(id, result)
	if err != nil {
		ref := a2a.ErrorRef()
		slog.Default().Warn("a2a.peer_error: marshal result", slog.String("ref", ref), slog.String("err", err.Error()))
		writeJSONRPCErr(w, id, a2a.JSONRPCErrInternal, a2a.PeerErrorMessage("internal error", ref))
		return
	}
	writeJSONRPCEnvelope(w, http.StatusOK, resp)
}

// writeJSONRPCErr is the sibling of writeJSONRPCResult. Always emits
// HTTP 200 — protocol errors ride the envelope, not the HTTP status.
func writeJSONRPCErr(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeJSONRPCEnvelope(w, http.StatusOK, a2a.NewErrorResponse(id, code, msg))
}

func writeJSONRPCEnvelope(w http.ResponseWriter, httpStatus int, resp a2a.JSONRPCResponse) {
	w.Header().Set("Content-Type", a2a.ContentTypeSpec)
	w.Header().Set("A2A-Version", a2a.SpecVersion)
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(resp) //nolint:errcheck // client-closed conn is not our problem
}
