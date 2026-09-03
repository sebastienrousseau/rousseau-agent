// Package control classifies inbound messages as either control verbs
// aimed at a turn already running, or ordinary prompt content, and
// tracks the running turn per conversation.
//
// # Scope
//
// This is deliberately the smallest surface that is useful. Four verbs,
// recognised only in their explicit slash form:
//
//	/status   report what the turn is doing right now
//	/pause    suspend at the next checkpoint
//	/resume   release a paused turn
//	/cancel   abort the turn
//
// # Why slash-only
//
// The obvious design is to also accept bare words: "stop", "wait",
// "cancel", "continue". It reads better and needs no syntax. It is also
// where every false positive lives.
//
// The costs are not symmetric. A false negative means the user types it
// again, with a slash this time. A false positive on /cancel destroys
// however many minutes of work were in flight, and a false positive on
// /pause silently strands a turn the user thinks is still running.
// "wait", "hold on", "stop", "continue" are all ordinary English that
// appear constantly in the middle of real requests, and no word-count
// heuristic separates "stop" (the command) from "stop" (the user
// changing their mind about a detail) reliably enough to bet four
// minutes of work on it.
//
// So the first cut of this feature does not guess. A slash prefix is an
// unambiguous signal of intent, costs the user one character, and makes
// the false-positive rate zero by construction rather than by tuning.
//
// Bare aliases can be added later, informed by what people actually
// type, and that is a much easier change to make than removing an
// alias that has already eaten someone's work.
package control

// Class decides behaviour: only ClassControl acts on a running turn.
type Class string

const (
	// ClassControl acts on the running turn and never reaches the LLM.
	ClassControl Class = "control"
	// ClassPrompt is ordinary content: a request for new work.
	ClassPrompt Class = "prompt"
)

// Verb is a canonical control verb.
type Verb string

// The control verbs. These four, and only these four, act on a running
// turn.
//
// The test for admitting a verb here: it must refer to the agent's own
// execution and take no object. "cancel" is complete on its own;
// "summarise" demands "summarise what?", and anything that demands an
// object is a request for new work rather than a command about the
// work already happening.
//
// "explain" was considered and rejected. Bare "explain" plausibly means
// "explain what you are doing" OR "explain the thing we were just
// discussing", and that ambiguity is exactly what this package is
// trying not to have. /status covers the useful half.
const (
	// VerbStatus reports what the turn is doing right now.
	VerbStatus Verb = "status"
	// VerbPause suspends the turn at its next checkpoint.
	VerbPause Verb = "pause"
	// VerbResume releases a paused turn.
	VerbResume Verb = "resume"
	// VerbCancel aborts the turn.
	VerbCancel Verb = "cancel"
)

// slashVerbs is the complete set of recognised control commands
// (both full form and shortcut). Every verb has a shortcut so
// the operator never has to type the full word to steer a turn.
//
// They are honoured whether or not a turn is running: a /status with
// nothing in flight is answered "nothing running", which is more useful
// than silence and cannot destroy anything.
//
// The shortcut set:
//   - /st for /status (avoids /s which is the router's /sessions)
//   - /p  for /pause
//   - /r  for /resume  — see the router package's commandAliases;
//     the router canonicalises /r → /resume BEFORE the message
//     reaches control.Decide, so both /r and /resume land here
//   - /x  for /cancel (avoids /c which is the router's /clear)
var slashVerbs = map[string]Verb{
	"/status": VerbStatus,
	"/st":     VerbStatus,
	"/pause":  VerbPause,
	"/p":      VerbPause,
	"/resume": VerbResume,
	"/cancel": VerbCancel,
	"/x":      VerbCancel,
}

// Verbs returns the recognised control commands keyed by their slash
// form, for documentation, help text and tests.
func Verbs() map[string]Verb {
	out := make(map[string]Verb, len(slashVerbs))
	for k, v := range slashVerbs {
		out[k] = v
	}
	return out
}
