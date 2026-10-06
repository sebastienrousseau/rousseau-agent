package slack

// maxResponseBody bounds how much of an API response is read into
// memory. A misbehaving proxy or endpoint cannot stream an unbounded
// body straight into the daemon's heap.
const maxResponseBody = 4 << 20

// maxFileBody bounds a file download. The media policy applies its
// own per-attachment caps afterwards; this is the hard ceiling so an
// oversized upload is cut off at the socket rather than buffered.
const maxFileBody = 64 << 20
