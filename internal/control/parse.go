package control

import "strings"

// Decision is the outcome of classifying one inbound message.
type Decision struct {
	// Class is the only field that changes behaviour.
	Class Class
	// Verb is the canonical control verb when Class is ClassControl.
	// Empty otherwise.
	Verb Verb
	// Prompt is the text to hand to the model when Class is
	// ClassPrompt. Empty when Class is ClassControl.
	Prompt string
	// Raw is the message exactly as received.
	Raw string
}

// IsControl reports whether the decision acts on the running turn
// instead of reaching the model.
func (d Decision) IsControl() bool { return d.Class == ClassControl }

// Decide classifies body.
//
// The rules are deliberately few, because every rule is a place a false
// positive can hide:
//
//   - A message that is exactly one of the control commands — "/status",
//     "/pause", "/resume", "/cancel", case-insensitively, with
//     surrounding whitespace ignored — is control.
//   - Everything else is a prompt. Including "/cancel now", including
//     bare "cancel", including "/whoami" and any other slash command,
//     which is left for the handlers that own it.
//
// Note the running state is deliberately not a parameter. Whether a
// turn is in flight is the registry's business; making it an input here
// would mean the same text classified two different ways depending on
// timing, which is the kind of behaviour that is impossible to reason
// about from a bug report.
//
// A control command that takes an argument is treated as a prompt
// rather than as the command, because "/cancel the deploy, not the
// build" is far more likely to be someone talking than someone issuing
// a command with a stray argument.
func Decide(body string) Decision {
	trimmed := strings.TrimSpace(body)
	d := Decision{Class: ClassPrompt, Prompt: trimmed, Raw: body}
	if trimmed == "" {
		return d
	}
	if v, ok := slashVerbs[strings.ToLower(trimmed)]; ok {
		return Decision{Class: ClassControl, Verb: v, Raw: body}
	}
	return d
}
