package sandbox

import (
	"bytes"
	"fmt"
	"os/exec"
	"time"
)

// DefaultOutputCap bounds the combined stdout+stderr a command may
// buffer in memory. Bytes past the cap are counted and discarded while
// the command runs, so a model running `yes` cannot grow the daemon's
// heap for the whole timeout.
const DefaultOutputCap = 1 << 20

// killWaitDelay bounds how long Wait keeps reading a pipe after the
// command was cancelled: a descendant that escaped the kill and still
// holds stdout cannot block the caller past this.
const killWaitDelay = 2 * time.Second

// CappedBuffer is an io.Writer that stores at most Max bytes and
// counts the rest. Write always reports success so the child never
// sees EPIPE-style errors and keeps its normal exit status.
//
// It is not safe for concurrent use; exec.Cmd serialises writes when
// the same pointer is assigned to both Stdout and Stderr.
type CappedBuffer struct {
	max     int
	buf     bytes.Buffer
	dropped int64
}

// NewCappedBuffer returns a buffer capped at maxBytes; a non-positive
// value uses DefaultOutputCap.
func NewCappedBuffer(maxBytes int) *CappedBuffer {
	if maxBytes <= 0 {
		maxBytes = DefaultOutputCap
	}
	return &CappedBuffer{max: maxBytes}
}

// Write stores what fits under the cap and drops the remainder.
func (c *CappedBuffer) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if room >= len(p) {
		c.buf.Write(p)
		return len(p), nil
	}
	if room > 0 {
		c.buf.Write(p[:room])
	} else {
		room = 0
	}
	c.dropped += int64(len(p) - room)
	return len(p), nil
}

// Dropped reports how many bytes were discarded.
func (c *CappedBuffer) Dropped() int64 { return c.dropped }

// String returns the captured bytes, followed by a truncation marker
// when anything was dropped.
func (c *CappedBuffer) String() string {
	if c.dropped == 0 {
		return c.buf.String()
	}
	return fmt.Sprintf("%s\n[output truncated: %d bytes dropped]", c.buf.String(), c.dropped)
}

// PrepareCommand hardens cmd's lifecycle before Start: on platforms
// with process groups the child leads its own group and cancellation
// kills the whole group, and WaitDelay stops a held pipe from blocking
// Wait indefinitely.
func PrepareCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = killWaitDelay
	setProcessGroup(cmd)
}
