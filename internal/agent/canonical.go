package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// ErrNonCanonicalInput reports tool input that is not valid JSON or
// repeats an object key. Such input is never approved or executed:
// approvers, hooks and tools could each read a different value out of
// it.
var ErrNonCanonicalInput = errors.New("tool input is not valid JSON or repeats a key")

// CanonicalInput returns the one byte form of a tool input that every
// consumer (approver, hook, tool) sees. It rejects invalid JSON and
// any object that repeats a key at any depth, then re-encodes the
// value with sorted keys, no insignificant whitespace, no HTML
// escaping and every string escape decoded, so a pattern written
// against the plain text cannot be dodged with a backslash-u escaped
// letter or a second "command" key. Numbers keep their literal form.
// Empty input is the empty object.
//
// Keys are compared case-insensitively (Unicode simple folding, the
// rule encoding/json applies when it decodes into a struct), so
// {"command":..,"Command":..} is a duplicate too.
func CanonicalInput(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if err := checkUniqueKeys(raw); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNonCanonicalInput, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNonCanonicalInput, err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNonCanonicalInput, err)
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// keyScope tracks the keys already seen in one open JSON container.
// keys is nil for an array.
type keyScope struct {
	keys      map[string]struct{}
	expectKey bool
}

// checkUniqueKeys walks raw token by token and fails on invalid JSON,
// trailing data, or a key repeated within one object.
func checkUniqueKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var stack []*keyScope
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if stack, err = stepKeys(stack, tok); err != nil {
			return err
		}
		if len(stack) == 0 && dec.More() {
			return errors.New("trailing data after the top-level value")
		}
	}
}

// stepKeys applies one token to the container stack.
func stepKeys(stack []*keyScope, tok json.Token) ([]*keyScope, error) {
	if len(stack) > 0 {
		if top := stack[len(stack)-1]; top.keys != nil {
			if top.expectKey {
				return objectKeyStep(stack, top, tok)
			}
			top.expectKey = true // this token is the value; a key follows
		}
	}
	return containerStep(stack, tok), nil
}

// objectKeyStep handles the token in key position of an object: the
// closing brace or a key.
func objectKeyStep(stack []*keyScope, top *keyScope, tok json.Token) ([]*keyScope, error) {
	if d, ok := tok.(json.Delim); ok && d == '}' {
		return stack[:len(stack)-1], nil
	}
	return stack, top.addKey(tok)
}

// containerStep opens or closes a container for a value-position
// token; scalars leave the stack alone.
func containerStep(stack []*keyScope, tok json.Token) []*keyScope {
	switch tok {
	case json.Delim('{'):
		return append(stack, &keyScope{keys: map[string]struct{}{}, expectKey: true})
	case json.Delim('['):
		return append(stack, &keyScope{})
	case json.Delim(']'):
		return stack[:len(stack)-1]
	}
	return stack
}

// addKey records one object key, failing when its fold is already
// present.
func (s *keyScope) addKey(tok json.Token) error {
	key, ok := tok.(string)
	if !ok {
		return fmt.Errorf("object key is %T, not a string", tok)
	}
	folded := foldKey(key)
	if _, dup := s.keys[folded]; dup {
		return fmt.Errorf("object repeats key %q", key)
	}
	s.keys[folded] = struct{}{}
	s.expectKey = false
	return nil
}

// foldKey maps every rune to the smallest member of its Unicode
// simple-fold orbit, so foldKey(a) == foldKey(b) exactly when
// strings.EqualFold(a, b).
func foldKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		low := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < low {
				low = f
			}
		}
		b.WriteRune(low)
	}
	return b.String()
}
