package transport

import (
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactURLError_PathAndQuery(t *testing.T) {
	inner := errors.New("connection refused")
	err := &url.Error{
		Op:  "Post",
		URL: "https://api.example/botFAKE-tok/getUpdates?password=FAKE-pw&x=FAKE-tok",
		Err: inner,
	}
	got := RedactURLError(err, "FAKE-tok", "FAKE-pw")

	var ue *url.Error
	require.ErrorAs(t, got, &ue)
	assert.Equal(t, "Post", ue.Op)
	assert.Equal(t, "https://api.example/bot***/getUpdates?password=***&x=***", ue.URL)
	assert.ErrorIs(t, got, inner, "the cause is kept")
	assert.NotContains(t, got.Error(), "FAKE-")
}

func TestRedactURLError_EscapedForms(t *testing.T) {
	secret := "a b/c&d"
	err := &url.Error{
		Op:  "Get",
		URL: "http://h/p/" + url.PathEscape(secret) + "?k=" + url.QueryEscape(secret),
		Err: errors.New("boom"),
	}
	got := RedactURLError(err, secret)
	assert.Equal(t, `Get "http://h/p/***?k=***": boom`, got.Error())
}

func TestRedactURLError_FindsWrappedURLError(t *testing.T) {
	err := fmt.Errorf("ctx: %w", &url.Error{Op: "Get", URL: "http://h/FAKE-tok", Err: errors.New("x")})
	got := RedactURLError(err, "FAKE-tok")
	assert.NotContains(t, got.Error(), "FAKE-tok")
}

func TestRedactURLError_PassesOtherErrorsThrough(t *testing.T) {
	plain := errors.New("plain FAKE-tok")
	assert.Same(t, plain, RedactURLError(plain, "FAKE-tok"))
	assert.NoError(t, RedactURLError(nil, "FAKE-tok"))
}

func TestRedactURLError_EmptySecretIgnored(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "http://h/p", Err: errors.New("x")}
	got := RedactURLError(err, "")
	assert.Equal(t, `Get "http://h/p": x`, got.Error())
}
