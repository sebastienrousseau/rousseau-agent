package client

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp"
)

// Frames larger than PIPE_BUF written from many goroutines must land
// on the pipe intact: every line the server reads is one complete
// JSON envelope. Without the write mutex the bytes of two frames
// interleave and the server mis-parses both.
func TestWrite_ConcurrentLargeFramesStayIntact(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	c := newTestClient(w)

	const (
		writers  = 16
		perWrite = 64
	)
	// Each payload is well over the 4 KiB atomic-write limit.
	big := strings.Repeat("x", 16*1024)

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perWrite; j++ {
				params, _ := json.Marshal(map[string]any{"id": id, "n": j, "pad": big}) //nolint:errcheck // static input
				env := mcp.Envelope{JSONRPC: "2.0", Method: "tools/call", Params: params}
				assert.NoError(t, c.write(env))
			}
		}(i)
	}

	readDone := make(chan int)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		n := 0
		for sc.Scan() {
			var env mcp.Envelope
			if err := json.Unmarshal(sc.Bytes(), &env); err != nil {
				t.Errorf("corrupt frame %d: %v", n, err)
			}
			n++
		}
		readDone <- n
	}()

	wg.Wait()
	require.NoError(t, w.Close())
	assert.Equal(t, writers*perWrite, <-readDone)
}
