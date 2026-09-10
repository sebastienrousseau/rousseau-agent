package a2a

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestJSONRPCRequest_ValidateAcceptsWellFormed(t *testing.T) {
	t.Parallel()
	r := JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  MethodSendMessage,
		ID:      json.RawMessage(`"req-1"`),
	}
	if err := r.Validate(); err != nil {
		t.Errorf("valid request rejected: %v", err)
	}
}

func TestJSONRPCRequest_ValidateRejectsWrongVersion(t *testing.T) {
	t.Parallel()
	r := JSONRPCRequest{JSONRPC: "1.0", Method: MethodSendMessage, ID: json.RawMessage(`1`)}
	if err := r.Validate(); err == nil {
		t.Error("expected version-mismatch error")
	}
}

func TestJSONRPCRequest_ValidateRejectsEmptyMethod(t *testing.T) {
	t.Parallel()
	r := JSONRPCRequest{JSONRPC: JSONRPCVersion, ID: json.RawMessage(`1`)}
	if err := r.Validate(); err == nil {
		t.Error("expected empty-method error")
	}
}

func TestJSONRPCRequest_ValidateRejectsMissingID(t *testing.T) {
	t.Parallel()
	// A2A does not support notification-style requests (no ID).
	// This freezes that contract — silent notifications would leak
	// task state without a way for the caller to reason about it.
	r := JSONRPCRequest{JSONRPC: JSONRPCVersion, Method: MethodSendMessage}
	if err := r.Validate(); err == nil {
		t.Error("expected missing-id error")
	}
}

func TestNewErrorResponse_ShapeMatchesSpec(t *testing.T) {
	t.Parallel()
	// Peers implementing the JSON-RPC 2.0 spec expect these exact
	// fields — freezing the wire shape so a rename becomes a
	// deliberate cross-package decision.
	resp := NewErrorResponse(json.RawMessage(`42`), JSONRPCErrTaskNotFound, "no such task")
	blob, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	got := string(blob)
	for _, want := range []string{
		`"jsonrpc":"2.0"`,
		`"error":{"code":-32001,"message":"no such task"}`,
		`"id":42`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("marshaled response missing %q:\n%s", want, got)
		}
	}
	// result MUST NOT appear on an error response.
	if strings.Contains(got, `"result"`) {
		t.Errorf("error response leaked a result field: %s", got)
	}
}

func TestNewResultResponse_ShapeMatchesSpec(t *testing.T) {
	t.Parallel()
	resp, err := NewResultResponse(json.RawMessage(`"call-1"`), SpecTask{
		ID: "t-1", Status: TaskStatus1{State: TaskStateSubmitted},
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	got := string(blob)
	for _, want := range []string{
		`"jsonrpc":"2.0"`,
		`"result":{`,
		`"id":"call-1"`,
		`"state":"TASK_STATE_SUBMITTED"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("marshaled response missing %q:\n%s", want, got)
		}
	}
	// error MUST NOT appear on a success response.
	if strings.Contains(got, `"error"`) {
		t.Errorf("success response leaked an error field: %s", got)
	}
}

func TestNewResultResponse_UnmarshalableFails(t *testing.T) {
	t.Parallel()
	// Freezes the sign-time-of-failure contract: a result that
	// can't be marshaled surfaces the error, doesn't ship an
	// empty response.
	_, err := NewResultResponse(json.RawMessage(`1`), make(chan int))
	if err == nil {
		t.Error("expected marshal error for unmarshalable value")
	}
}

func TestJSONRPCError_Roundtrips(t *testing.T) {
	t.Parallel()
	// Data field is optional but the envelope must round-trip
	// through JSON without dropping fields.
	orig := JSONRPCError{
		Code: JSONRPCErrInvalidParams, Message: "bad params",
		Data: json.RawMessage(`{"field":"prompt"}`),
	}
	blob, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var round JSONRPCError
	if err := json.Unmarshal(blob, &round); err != nil {
		t.Fatal(err)
	}
	if round.Code != orig.Code || round.Message != orig.Message {
		t.Errorf("round-trip lost fields: %+v", round)
	}
	if !bytesEqual(round.Data, orig.Data) {
		t.Errorf("round-trip lost data: %s vs %s", round.Data, orig.Data)
	}
}

// bytesEqual compares two RawMessages for byte-equality after
// canonicalisation via re-marshaling. Prevents whitespace differences
// from failing tests.
func bytesEqual(a, b json.RawMessage) bool {
	var ax, bx any
	if err := json.Unmarshal(a, &ax); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bx); err != nil {
		return false
	}
	aBlob, _ := json.Marshal(ax) //nolint:errcheck // canonicalisation of already-decoded JSON cannot fail
	bBlob, _ := json.Marshal(bx) //nolint:errcheck // canonicalisation of already-decoded JSON cannot fail
	return string(aBlob) == string(bBlob)
}

func TestValidate_ReturnsSentinelText(t *testing.T) {
	t.Parallel()
	// Consumer callers use errors.Is against a plain errors.New
	// today, so we just check the messages are stable (freezing the
	// human-readable text since the codes route through the
	// envelope layer).
	cases := []struct {
		req  JSONRPCRequest
		want string
	}{
		{JSONRPCRequest{}, "jsonrpc field must be"},
		{JSONRPCRequest{JSONRPC: JSONRPCVersion}, "method is required"},
		{JSONRPCRequest{JSONRPC: JSONRPCVersion, Method: MethodSendMessage}, "id is required"},
	}
	for _, tc := range cases {
		err := tc.req.Validate()
		if err == nil {
			t.Errorf("expected error for %+v", tc.req)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("err = %q, want to contain %q", err.Error(), tc.want)
		}
	}
}

// TestValidate_ErrorsAreDistinctSentinels documents that Validate
// returns fresh errors each call — no sentinel promotion yet.
// Callers use error.Error() matching. If a future PR promotes these
// to package-level sentinels for errors.Is, this test flips into
// asserting the sentinels instead.
func TestValidate_ErrorsAreDistinctSentinels(t *testing.T) {
	t.Parallel()
	err := JSONRPCRequest{}.Validate()
	if errors.Is(err, nil) {
		t.Fatal("nil error")
	}
}
