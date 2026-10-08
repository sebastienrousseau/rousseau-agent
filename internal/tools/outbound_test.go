package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type outboundTool struct {
	fakeTool
	out bool
}

func (o *outboundTool) Outbound() bool { return o.out }

func TestIsOutbound(t *testing.T) {
	assert.False(t, IsOutbound(&fakeTool{n: "read"}), "a tool without Outbound is not outbound")
	assert.False(t, IsOutbound(&outboundTool{fakeTool: fakeTool{n: "x"}, out: false}))
	assert.True(t, IsOutbound(&outboundTool{fakeTool: fakeTool{n: "send"}, out: true}))
}
