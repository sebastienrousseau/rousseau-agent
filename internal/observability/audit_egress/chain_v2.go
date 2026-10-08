package audit_egress

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Chain hash encodings. A record's [ChainInfo.Version] names the one
// its Hash was computed with; 0 is read as [ChainVersionV1] so records
// written before the field existed still verify.
const (
	// ChainVersionV1 is the original encoding: fields joined with a
	// NUL byte. A NUL inside a field can move a boundary, so two
	// different records can share a hash. Verified, never written.
	ChainVersionV1 = 1
	// ChainVersionV2 length-prefixes every field and hashes Detail as
	// canonical JSON, rejecting invalid UTF-8. See [hashInputV2].
	ChainVersionV2 = 2
)

// detailErrorKey replaces a Detail that cannot be hashed under v2.
const detailErrorKey = "detail_error"

// maxDetailDepth bounds the UTF-8 walk over Detail values (and so any
// pointer cycle in them).
const maxDetailDepth = 64

var (
	errDetailInvalidUTF8 = errors.New("detail contains invalid UTF-8")
	errDetailTooDeep     = errors.New("detail nests too deeply")
)

// hashInputV2 is the byte string a v2 record's Hash covers:
//
//	0x02 ‖ for each field: uvarint(len(field)) ‖ field
//
// over Sequence (decimal), PrevHash, At (UTC Unix nanoseconds,
// decimal), Category, Actor, Verb, Object, Result, TraceID and the
// canonical JSON of Detail ([canonicalDetailV2]). The length prefix
// makes the encoding injective: no choice of field contents can move a
// boundary. The field order MUST NEVER change; a new layout is a new
// version.
func hashInputV2(r Record) ([]byte, error) {
	detail, err := canonicalDetailV2(r.Detail)
	if err != nil {
		return nil, err
	}
	fields := [][]byte{
		[]byte(strconv.FormatUint(r.Chain.Sequence, 10)),
		[]byte(r.Chain.PrevHash),
		[]byte(strconv.FormatInt(r.At.UTC().UnixNano(), 10)),
		[]byte(r.Category),
		[]byte(r.Actor),
		[]byte(r.Verb),
		[]byte(r.Object),
		[]byte(r.Result),
		[]byte(r.TraceID),
		detail,
	}
	out := []byte{ChainVersionV2}
	for _, f := range fields {
		out = binary.AppendUvarint(out, uint64(len(f)))
		out = append(out, f...)
	}
	return out, nil
}

// canonicalHashV2 is the hex SHA-256 of [hashInputV2].
func canonicalHashV2(r Record) (string, error) {
	in, err := hashInputV2(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(in)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalDetailV2 renders Detail as canonical JSON: every object's
// keys sorted at every depth (struct values included, by decoding the
// marshalled form back into maps), numbers kept as written. It is a
// fixed point, so a verifier that decodes the exported detail and
// renders it again gets the same bytes.
//
// Invalid UTF-8 anywhere in the value is an error: json.Marshal would
// silently replace it with U+FFFD, so two different details would
// hash alike. Unmarshalable values (channels, NaN) are errors too.
func canonicalDetailV2(d map[string]any) ([]byte, error) {
	if len(d) == 0 {
		return []byte("{}"), nil
	}
	if err := checkUTF8(reflect.ValueOf(d), 0); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("detail: %w", err)
	}
	if !utf8.Valid(raw) {
		return nil, errDetailInvalidUTF8
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("detail: %w", err)
	}
	out, err := json.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("detail: %w", err)
	}
	return out, nil
}

// checkUTF8 reports invalid UTF-8 in any string reachable from v that
// json.Marshal would encode: map keys and values, slice and array
// elements, exported struct fields, and through pointers and
// interfaces.
func checkUTF8(v reflect.Value, depth int) error {
	if depth > maxDetailDepth {
		return errDetailTooDeep
	}
	switch v.Kind() {
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return errDetailInvalidUTF8
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return checkUTF8(v.Elem(), depth+1)
		}
	case reflect.Map:
		return checkUTF8Map(v, depth)
	case reflect.Slice, reflect.Array:
		return checkUTF8Seq(v, depth)
	case reflect.Struct:
		return checkUTF8Struct(v, depth)
	}
	return nil
}

func checkUTF8Map(v reflect.Value, depth int) error {
	it := v.MapRange()
	for it.Next() {
		if err := checkUTF8(it.Key(), depth+1); err != nil {
			return err
		}
		if err := checkUTF8(it.Value(), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func checkUTF8Seq(v reflect.Value, depth int) error {
	if v.Type().Elem().Kind() == reflect.Uint8 {
		return nil // []byte marshals as base64
	}
	for i := range v.Len() {
		if err := checkUTF8(v.Index(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func checkUTF8Struct(v reflect.Value, depth int) error {
	t := v.Type()
	for i := range t.NumField() {
		if !t.Field(i).IsExported() {
			continue
		}
		if err := checkUTF8(v.Field(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

// prepareV2 makes rec hashable and exportable under v2 before it is
// stamped: top-level strings are made valid UTF-8 (the OTLP JSON
// would replace invalid bytes anyway, and the hash must cover what
// the SIEM receives), and a Detail that cannot be rendered
// canonically is replaced by {"detail_error": reason}.
func prepareV2(rec *Record) {
	for _, s := range []*string{&rec.Category, &rec.Actor, &rec.Verb, &rec.Object, &rec.Result, &rec.TraceID} {
		*s = strings.ToValidUTF8(*s, "�")
	}
	if _, err := canonicalDetailV2(rec.Detail); err != nil {
		rec.Detail = map[string]any{detailErrorKey: strings.ToValidUTF8(err.Error(), "�")}
	}
}

// recordHash recomputes rec's Hash under the encoding its Version
// names.
func recordHash(rec Record) (string, error) {
	switch rec.Chain.Version {
	case 0, ChainVersionV1:
		return canonicalHash(rec), nil
	case ChainVersionV2:
		return canonicalHashV2(rec)
	default:
		return "", fmt.Errorf("unsupported chain version %d", rec.Chain.Version)
	}
}

// effectiveVersion maps the legacy zero value to v1.
func effectiveVersion(v uint8) uint8 {
	if v == 0 {
		return ChainVersionV1
	}
	return v
}
