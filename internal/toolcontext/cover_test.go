package toolcontext_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/toolcontext"
)

// TestWithProvider_NilIsIdentity proves a nil provider does not install
// an empty value that later reads would mistake for a real provider.
func TestWithProvider_NilIsIdentity(t *testing.T) {
	base := context.Background()
	derived := toolcontext.WithProvider(base, nil)
	assert.Equal(t, base, derived)

	got, ok := toolcontext.Provider(derived)
	assert.False(t, ok)
	assert.Nil(t, got)
}
