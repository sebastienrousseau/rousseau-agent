package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// Regression tests for `rousseau skills validate`. Locks in the
// spec-compliant validator surface: valid skills pass, invalid
// skills fail with legible error messages, exit code + JSON shape
// are stable enough for a git pre-commit hook or CI gate to
// script against.

// writeValidateSkill is a validate_test-scoped helper to avoid
// clashing with the existing writeSpecSkill helper used by
// discover_test.go — Go test binaries treat both files as one
// package and would otherwise reject the duplicate.
func writeValidateSkill(t *testing.T, dir, name, description, extras string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := "---\nname: " + name + "\ndescription: " + description + "\n"
	if extras != "" {
		body += extras + "\n"
	}
	body += "---\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600))
}

// -- resolveValidateDir ----------------------------------------------

func TestResolveValidateDir_ExplicitArgWins(t *testing.T) {
	opts := &Options{
		Config: &config.Config{
			Agent: config.AgentConfig{SkillsDir: "/config/dir"},
		},
	}
	got := resolveValidateDir(opts, []string{"/cli/dir"})
	assert.Equal(t, "/cli/dir", got, "positional argument must override the config default")
}

func TestResolveValidateDir_EmptyArgFallsBackToConfig(t *testing.T) {
	opts := &Options{
		Config: &config.Config{
			Agent: config.AgentConfig{SkillsDir: "/config/dir"},
		},
	}
	got := resolveValidateDir(opts, nil)
	assert.Equal(t, "/config/dir", got)

	// An empty-string positional arg also falls through.
	got = resolveValidateDir(opts, []string{""})
	assert.Equal(t, "/config/dir", got)
}

// -- runSkillsValidate — human mode ----------------------------------

func TestRunSkillsValidate_HappyPathHuman(t *testing.T) {
	dir := t.TempDir()
	writeValidateSkill(t, filepath.Join(dir, "greet"), "greet", "Greet the user warmly.", "")

	var stdout, stderr bytes.Buffer
	err := runSkillsValidate(&stdout, &stderr, dir, false)
	require.NoError(t, err, "single valid skill must exit 0")

	// Skill line lives on stdout so `| grep ✔` remains scriptable.
	assert.Contains(t, stdout.String(), "✔ greet")
	assert.Contains(t, stdout.String(), "Greet the user warmly.")
	// Header + summary land on stderr (operator commentary, not data).
	assert.Contains(t, stderr.String(), "Validating skills under")
	assert.Contains(t, stderr.String(), "1 valid, 0 invalid")
}

func TestRunSkillsValidate_MixedValidAndInvalidHuman(t *testing.T) {
	dir := t.TempDir()
	writeValidateSkill(t, filepath.Join(dir, "greet"), "greet", "OK.", "")
	writeValidateSkill(t, filepath.Join(dir, "typoed"), "typoed", "bad", "descripton: typo")
	writeValidateSkill(t, filepath.Join(dir, "mismatch"), "wrong-name", "mismatch", "")

	var stdout, stderr bytes.Buffer
	err := runSkillsValidate(&stdout, &stderr, dir, false)
	require.Error(t, err, "any invalid skill must surface a non-nil error so cobra exits non-zero")
	assert.Contains(t, err.Error(), "2 skill(s) failed validation")

	assert.Contains(t, stdout.String(), "✔ greet")
	assert.Contains(t, stdout.String(), `unknown field "descripton"`)
	assert.Contains(t, stdout.String(), "does not match containing directory")
	assert.Contains(t, stderr.String(), "Summary: 1 valid, 2 invalid (3 total)")
}

func TestRunSkillsValidate_InvalidSkillWithoutName_UsesDirName(t *testing.T) {
	dir := t.TempDir()
	// Skill dir "nofrontmatter" with a body but no frontmatter —
	// Name is empty in the validation record, so labelForInvalid
	// falls back to the parent dir name.
	subDir := filepath.Join(dir, "nofrontmatter")
	require.NoError(t, os.MkdirAll(subDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(subDir, "SKILL.md"),
		[]byte("just body, no frontmatter\n"),
		0o600,
	))

	var stdout, stderr bytes.Buffer
	err := runSkillsValidate(&stdout, &stderr, dir, false)
	require.Error(t, err)
	assert.Contains(t, stdout.String(), "✘ nofrontmatter",
		"missing name → dir name used as the failing skill's label")
}

func TestRunSkillsValidate_LongDescriptionTruncated(t *testing.T) {
	dir := t.TempDir()
	// >70 rune description forces the truncation branch.
	longDesc := strings.Repeat("x", 100)
	writeValidateSkill(t, filepath.Join(dir, "longone"), "longone", longDesc, "")

	var stdout, stderr bytes.Buffer
	err := runSkillsValidate(&stdout, &stderr, dir, false)
	require.NoError(t, err)
	line := stdout.String()
	assert.Contains(t, line, "…", "truncation must add the ellipsis")
	assert.NotContains(t, line, strings.Repeat("x", 100),
		"the full 100-rune description must not appear")
}

func TestRunSkillsValidate_EmptyDirEmptyOutput(t *testing.T) {
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	err := runSkillsValidate(&stdout, &stderr, dir, false)
	require.NoError(t, err, "empty dir is a valid state — no skills is not a failure")
	assert.Contains(t, stderr.String(), "no skills found")
	assert.Empty(t, stdout.String(), "no data lines when there are no skills")
}

// -- runSkillsValidate — JSON mode -----------------------------------

func TestRunSkillsValidate_JSONShape(t *testing.T) {
	dir := t.TempDir()
	writeValidateSkill(t, filepath.Join(dir, "alpha"), "alpha", "the alpha skill", "")
	writeValidateSkill(t, filepath.Join(dir, "beta"), "wrong-name", "invalid", "")

	var stdout, stderr bytes.Buffer
	err := runSkillsValidate(&stdout, &stderr, dir, true)
	require.Error(t, err, "any invalid record must surface an error")

	var got []validationRecord
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	require.Len(t, got, 2)

	// Deterministic sort by path — alpha < beta lexicographically.
	assert.Equal(t, "alpha", got[0].Name)
	assert.True(t, got[0].OK)
	assert.Equal(t, "the alpha skill", got[0].Description)
	assert.Empty(t, got[0].Err)

	assert.False(t, got[1].OK)
	assert.Empty(t, got[1].Name, "failed records omit Name when the parser didn't reach it")
	assert.Contains(t, got[1].Err, "does not match containing directory")
}

func TestRunSkillsValidate_JSONEmptyDirIsEmptyArray(t *testing.T) {
	// `jq length` should return 0, not "null", so we emit an empty
	// array rather than a JSON null literal for a dir with no
	// discoverable skills.
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	err := runSkillsValidate(&stdout, &stderr, dir, true)
	require.NoError(t, err)

	body := strings.TrimSpace(stdout.String())
	assert.Equal(t, "[]", body, "empty JSON output must be '[]', not 'null'")
	_ = stderr
}

// -- Error surfaces ---------------------------------------------------

func TestRunSkillsValidate_EmptyDirArgErrors(t *testing.T) {
	err := runSkillsValidate(&bytes.Buffer{}, &bytes.Buffer{}, "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no skills directory to validate")
}

func TestRunSkillsValidate_MissingDirErrors(t *testing.T) {
	err := runSkillsValidate(&bytes.Buffer{}, &bytes.Buffer{},
		"/definitely/does/not/exist/rousseau-validate-tests", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

// -- Command wiring ---------------------------------------------------

func TestNewSkillsValidateCmd_Registered(t *testing.T) {
	// End-to-end: newSkillsCmd exposes `validate` as a subcommand
	// so the operator can discover it via `rousseau skills --help`.
	opts := &Options{Config: &config.Config{}}
	skillsCmd := newSkillsCmd(opts)

	var found bool
	for _, sub := range skillsCmd.Commands() {
		if sub.Name() == "validate" {
			found = true
			break
		}
	}
	assert.True(t, found, "newSkillsCmd must register the validate subcommand")
}

// -- Small helpers ---------------------------------------------------

func TestTruncateForList_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty passes through", "", 10, ""},
		{"short passes through", "abc", 10, "abc"},
		{"exactly at limit", "0123456789", 10, "0123456789"},
		{"one over gets ellipsis", "0123456789x", 10, "0123456789…"},
		{"zero n returns empty", "abc", 0, ""},
		{"negative n returns empty", "abc", -5, ""},
		{"multi-byte utf8 cut on rune boundary", "héllo wörld!", 5, "héllo…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, truncateForList(tc.in, tc.n))
		})
	}
}

func TestLabelForInvalid_UsesNameWhenSet(t *testing.T) {
	r := validationRecord{Name: "explicit", Path: "/skills/somedir/SKILL.md"}
	assert.Equal(t, "explicit", labelForInvalid(r))
}

func TestLabelForInvalid_FallsBackToDirName(t *testing.T) {
	r := validationRecord{Path: "/skills/mydir/SKILL.md"}
	assert.Equal(t, "mydir", labelForInvalid(r))
}
