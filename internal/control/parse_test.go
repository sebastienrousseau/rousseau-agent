package control

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecide_RecognisesEverySlashVerb(t *testing.T) {
	for slash, want := range Verbs() {
		t.Run(slash, func(t *testing.T) {
			d := Decide(slash)
			require.True(t, d.IsControl(), "%s must be control", slash)
			assert.Equal(t, want, d.Verb)
			assert.Empty(t, d.Prompt, "a control decision carries no prompt")
			assert.Equal(t, slash, d.Raw)
		})
	}
}

func TestDecide_IsCaseAndWhitespaceInsensitive(t *testing.T) {
	for _, in := range []string{"/CANCEL", "/Cancel", "  /cancel  ", "\t/cancel\n"} {
		t.Run(in, func(t *testing.T) {
			d := Decide(in)
			require.True(t, d.IsControl())
			assert.Equal(t, VerbCancel, d.Verb)
			assert.Equal(t, in, d.Raw, "Raw preserves exactly what arrived")
		})
	}
}

// TestDecide_BareWordsAreNeverControl is the core safety property of
// this package. Every string below is a plausible thing to type in the
// middle of a real conversation, and acting on any of them would either
// destroy work in flight or silently strand a turn.
func TestDecide_BareWordsAreNeverControl(t *testing.T) {
	for _, body := range []string{
		"cancel",
		"stop",
		"wait",
		"hold on",
		"pause",
		"resume",
		"continue",
		"status",
		"abort",
		"nevermind",
		"never mind",
		"forget it",
		"halt",
		"eta",
		"?",
		"go on",
		"STOP",
		"Cancel.",
		"cancel!",
	} {
		t.Run(body, func(t *testing.T) {
			d := Decide(body)
			assert.False(t, d.IsControl(),
				"%q must reach the model, not interrupt the turn", body)
			assert.Equal(t, ClassPrompt, d.Class)
			assert.Equal(t, body, d.Prompt)
		})
	}
}

// TestDecide_SlashVerbsWithArgumentsArePrompts guards the other half of
// the ambiguity: a command shape carrying an object is someone talking,
// not someone commanding.
func TestDecide_SlashVerbsWithArgumentsArePrompts(t *testing.T) {
	for _, body := range []string{
		"/cancel the deploy, not the build",
		"/status of the migration please",
		"/pause after this file",
		"/resume where we left off",
	} {
		t.Run(body, func(t *testing.T) {
			d := Decide(body)
			assert.False(t, d.IsControl(),
				"%q carries an argument and must be treated as content", body)
			assert.Equal(t, body, d.Prompt)
		})
	}
}

func TestDecide_LeavesOtherSlashCommandsAlone(t *testing.T) {
	// /whoami, /link and friends belong to other handlers. This package
	// must not swallow them.
	for _, body := range []string{"/whoami", "/link", "/unlink", "/help", "/"} {
		t.Run(body, func(t *testing.T) {
			d := Decide(body)
			assert.False(t, d.IsControl())
			assert.Equal(t, body, d.Prompt)
			assert.Empty(t, d.Verb)
		})
	}
}

func TestDecide_EmptyAndWhitespace(t *testing.T) {
	for _, body := range []string{"", "   ", "\n\t "} {
		d := Decide(body)
		assert.False(t, d.IsControl())
		assert.Empty(t, d.Prompt)
		assert.Equal(t, body, d.Raw)
	}
}

func TestDecide_OrdinaryPromptsPassThroughTrimmed(t *testing.T) {
	d := Decide("  summarise the last thread and cancel nothing  ")
	assert.False(t, d.IsControl())
	assert.Equal(t, "summarise the last thread and cancel nothing", d.Prompt)
	assert.Equal(t, "  summarise the last thread and cancel nothing  ", d.Raw)
}

func TestDecide_IsDeterministic(t *testing.T) {
	// Classification must not depend on whether a turn happens to be
	// running: the same text always means the same thing, so a bug
	// report is reproducible without knowing the timing.
	const body = "cancel"
	first := Decide(body)
	second := Decide(body)
	assert.Equal(t, first, second)
}

func TestVerbs_ReturnsACopy(t *testing.T) {
	got := Verbs()
	require.Len(t, got, 4, "the first cut ships exactly four control verbs")
	got["/wipe"] = Verb("wipe")
	assert.NotContains(t, Verbs(), "/wipe", "Verbs must not expose the internal map")
}

func TestVerbs_ContainsExactlyTheDocumentedSet(t *testing.T) {
	assert.Equal(t, map[string]Verb{
		"/status": VerbStatus,
		"/pause":  VerbPause,
		"/resume": VerbResume,
		"/cancel": VerbCancel,
	}, Verbs())
}
