package transport

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFetchPattern(t *testing.T) {
	p := FetchPattern("https", "cdn.example.com", "media.example.net")
	for raw, want := range map[string]bool{
		"https://cdn.example.com/a/b.png":       true,
		"https://media.example.net/a?x=1":       true,
		"https://cdn.example.com":               false, // no path: never an attachment
		"http://cdn.example.com/a":              false,
		"HTTPS://cdn.example.com/a":             false,
		"https://CDN.example.com/a":             false,
		"https://cdn.example.com:8443/a":        false,
		"https://user@cdn.example.com/a":        false,
		"https://cdn.example.com.evil.test/a":   false,
		"https://cdn.example.com\\@evil.test/a": false,
		"https://evil.test/cdn.example.com/a":   false,
		"https://cdnXexample.com/a":             false, // dots are literal
		"":                                      false,
	} {
		assert.Equal(t, want, p.MatchString(raw), "%q", raw)
	}
	assert.False(t, p.MatchString(" "+"https://cdn.example.com/a"), "leading space")
}

func TestFetchPattern_HostWithPort(t *testing.T) {
	// httptest servers listen on 127.0.0.1:port; tests trust exactly that.
	p := FetchPattern("http", "127.0.0.1:41234")
	assert.True(t, p.MatchString("http://127.0.0.1:41234/f"))
	assert.False(t, p.MatchString("http://127.0.0.1:41235/f"))
	assert.False(t, p.MatchString("http://127.0.0.1/f"))
}
