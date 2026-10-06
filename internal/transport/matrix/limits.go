package matrix

// maxResponseBody bounds how much of an API response is read into
// memory. A misbehaving proxy or endpoint cannot stream an unbounded
// body straight into the daemon's heap.
const maxResponseBody = 4 << 20
