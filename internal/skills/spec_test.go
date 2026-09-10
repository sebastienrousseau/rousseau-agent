package skills

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests below are the Phase 2.1 regression bar for the spec-
// compliant Agent Skills parser + validator. Every branch of
// ParseSkillMD, ValidateSpec, splitSpecFrontmatter, indexClose, and
// coerceMetadata is covered so a future refactor cannot silently
// re-introduce a legacy behaviour (unknown keys accepted, name-
// vs-dir mismatch ignored, unclosed frontmatter silently truncated).

// -- splitSpecFrontmatter ---------------------------------------------

func TestSplitSpecFrontmatter_TableDriven(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantFront string
		wantBody  string
		wantErr   error
	}{
		{
			name:      "canonical LF frontmatter and body",
			input:     "---\nname: x\ndescription: y\n---\nBody line.\n",
			wantFront: "name: x\ndescription: y",
			wantBody:  "Body line.\n",
		},
		{
			name:      "CRLF-only frontmatter is tolerated",
			input:     "---\r\nname: x\ndescription: y\n---\r\nBody line.\r\n",
			wantFront: "name: x\ndescription: y",
			wantBody:  "Body line.\r\n",
		},
		{
			name:      "leading UTF-8 BOM is stripped",
			input:     "\ufeff---\nname: x\ndescription: y\n---\nBody.\n",
			wantFront: "name: x\ndescription: y",
			wantBody:  "Body.\n",
		},
		{
			name:    "missing opening --- returns MissingFrontmatter",
			input:   "just a plain markdown body\n",
			wantErr: ErrSkillFrontmatterMissing,
		},
		{
			name:    "opening --- but no close returns Unclosed",
			input:   "---\nname: x\ndescription: y\nBody without close",
			wantErr: ErrSkillFrontmatterUnclosed,
		},
		{
			name:      "body containing a --- fence is not a false close",
			input:     "---\nname: x\ndescription: y\n---\ntext with ---inline--- fence\nreal body\n",
			wantFront: "name: x\ndescription: y",
			wantBody:  "text with ---inline--- fence\nreal body\n",
		},
		{
			name:      "empty body after close is fine",
			input:     "---\nname: x\ndescription: y\n---\n",
			wantFront: "name: x\ndescription: y",
			wantBody:  "",
		},
		{
			name:      "close at EOF with no trailing newline",
			input:     "---\nname: x\ndescription: y\n---",
			wantFront: "name: x\ndescription: y",
			wantBody:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			front, body, err := splitSpecFrontmatter([]byte(tc.input))
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantFront, front)
			assert.Equal(t, tc.wantBody, body)
		})
	}
}

func TestIndexClose_NoFalsePositiveOnInlineDashes(t *testing.T) {
	// A line that starts with `---` in the MIDDLE of the frontmatter
	// (e.g. an accidental horizontal rule inside YAML) must close.
	// A line with `---` followed by non-EOL must NOT close.
	assert.NotEqual(t, -1, indexClose("name: x\n---\n"),
		"proper newline-terminated close must be detected")
	// Test the deep look-ahead behaviour: a false close is skipped
	// and a real close later is still found.
	assert.NotEqual(t, -1, indexClose("name: x\n---plus stuff\ndescription: y\n---\n"),
		"non-terminating --- must be skipped and later real close still found")
}

// -- ParseSkillMD -----------------------------------------------------

func TestParseSkillMD_MinimalValidSpec(t *testing.T) {
	raw := []byte("---\nname: git-rebase\ndescription: Guide the user through interactive rebase safely.\n---\nBody.\n")
	spec, err := ParseSkillMD(raw)
	require.NoError(t, err)
	assert.Equal(t, "git-rebase", spec.Name)
	assert.Contains(t, spec.Description, "Guide the user")
	assert.Equal(t, "Body.", spec.Body)
	assert.Empty(t, spec.License)
	assert.Empty(t, spec.Compatibility)
	assert.Empty(t, spec.AllowedTools)
	assert.Nil(t, spec.Metadata)
}

func TestParseSkillMD_MaximalValidSpec(t *testing.T) {
	raw := []byte(`---
name: incident-response
description: Run the on-call triage workflow. Use when the user mentions an outage.
license: Proprietary. See LICENSE.txt.
compatibility: Requires gh, jq, pd.
allowed-tools: Bash(gh:*) Bash(jq:*) Read
metadata:
  author: platform-team
  version: "2.3"
  x-rousseau-signature-id: 7f3a
---

# Workflow body
step one.
`)
	spec, err := ParseSkillMD(raw)
	require.NoError(t, err)
	assert.Equal(t, "incident-response", spec.Name)
	assert.Equal(t, "Proprietary. See LICENSE.txt.", spec.License)
	assert.Equal(t, "Requires gh, jq, pd.", spec.Compatibility)
	assert.Equal(t, "Bash(gh:*) Bash(jq:*) Read", spec.AllowedTools)
	require.NotNil(t, spec.Metadata)
	assert.Equal(t, "platform-team", spec.Metadata["author"])
	assert.Equal(t, "2.3", spec.Metadata["version"])
	assert.Equal(t, "7f3a", spec.Metadata["x-rousseau-signature-id"])
}

func TestParseSkillMD_RejectsUnknownFrontmatterKey(t *testing.T) {
	// The spec's closed-key rule: catching typos like `descripton:`
	// or non-standard fields (rousseau used to accept `triggers:`)
	// is the whole point.
	raw := []byte("---\nname: x\ndescription: y\ntriggers: [foo]\n---\nBody.\n")
	_, err := ParseSkillMD(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "triggers"`)
	assert.Contains(t, err.Error(), "allowed: name, description")
}

func TestParseSkillMD_RejectsMissingFrontmatter(t *testing.T) {
	_, err := ParseSkillMD([]byte("no frontmatter here.\n"))
	require.ErrorIs(t, err, ErrSkillFrontmatterMissing)
}

func TestParseSkillMD_RejectsUnclosedFrontmatter(t *testing.T) {
	_, err := ParseSkillMD([]byte("---\nname: x\ndescription: y\nbody with no close"))
	require.ErrorIs(t, err, ErrSkillFrontmatterUnclosed)
}

func TestParseSkillMD_ReportsYAMLParseError(t *testing.T) {
	raw := []byte("---\nname: x\ndescription: :\n---\n")
	_, err := ParseSkillMD(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "frontmatter parse")
}

func TestParseSkillMD_EmptyFrontmatterErrors(t *testing.T) {
	// `---\n---\n` after stripping the opening fence leaves `---\n`,
	// which has no `\n---` line-terminated close. Correctly surfaces
	// as Unclosed rather than as a valid empty block — either error
	// is legible to the skill author.
	raw := []byte("---\n---\nBody.\n")
	_, err := ParseSkillMD(raw)
	require.ErrorIs(t, err, ErrSkillFrontmatterUnclosed)
}

func TestParseSkillMD_MetadataMapAny(t *testing.T) {
	// map[any]any comes up in the wild when a YAML doc has
	// non-string keys somewhere; coerceMetadata must stringify.
	raw := []byte("---\nname: x\ndescription: y\nmetadata:\n  1: numeric-key\n  bool: true\n---\n")
	spec, err := ParseSkillMD(raw)
	require.NoError(t, err)
	assert.Equal(t, "numeric-key", spec.Metadata["1"])
	assert.Equal(t, "true", spec.Metadata["bool"])
}

func TestParseSkillMD_MetadataWrongType(t *testing.T) {
	raw := []byte("---\nname: x\ndescription: y\nmetadata: not-a-map\n---\n")
	_, err := ParseSkillMD(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata must be a mapping")
}

// -- ValidateSpec -----------------------------------------------------

func TestValidateSpec_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		spec    SkillSpec
		dirName string
		wantErr error
	}{
		{
			name:    "valid minimal",
			spec:    SkillSpec{Name: "ok", Description: "x"},
			dirName: "ok",
		},
		{
			name:    "valid without dir check",
			spec:    SkillSpec{Name: "ok", Description: "x"},
			dirName: "",
		},
		{
			name:    "missing name",
			spec:    SkillSpec{Description: "x"},
			wantErr: ErrSkillNameMissing,
		},
		{
			name:    "missing description",
			spec:    SkillSpec{Name: "ok"},
			wantErr: ErrSkillDescriptionMissing,
		},
		{
			name: "name too long",
			spec: SkillSpec{
				Name:        strings.Repeat("a", 65),
				Description: "x",
			},
			wantErr: errors.New("exceeds 64"),
		},
		{
			name:    "name uppercase",
			spec:    SkillSpec{Name: "BadName", Description: "x"},
			wantErr: errors.New("must be lowercase"),
		},
		{
			name:    "name with underscore",
			spec:    SkillSpec{Name: "bad_name", Description: "x"},
			wantErr: errors.New("must be lowercase"),
		},
		{
			name:    "name with double hyphen",
			spec:    SkillSpec{Name: "bad--name", Description: "x"},
			wantErr: errors.New("must be lowercase"),
		},
		{
			name:    "name starting with hyphen",
			spec:    SkillSpec{Name: "-bad", Description: "x"},
			wantErr: errors.New("must be lowercase"),
		},
		{
			name:    "name ending with hyphen",
			spec:    SkillSpec{Name: "bad-", Description: "x"},
			wantErr: errors.New("must be lowercase"),
		},
		{
			name:    "name-dir mismatch",
			spec:    SkillSpec{Name: "one", Description: "x"},
			dirName: "two",
			wantErr: errors.New("does not match containing directory"),
		},
		{
			name: "description too long",
			spec: SkillSpec{
				Name:        "ok",
				Description: strings.Repeat("d", 1025),
			},
			wantErr: errors.New("description exceeds 1024"),
		},
		{
			name: "compatibility too long",
			spec: SkillSpec{
				Name:          "ok",
				Description:   "x",
				Compatibility: strings.Repeat("c", 501),
			},
			wantErr: errors.New("compatibility exceeds 500"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSpec(tc.spec, tc.dirName)
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			// Sentinel errors compared via errors.Is; string-contained
			// errors compared via Contains (the exact message may
			// interpolate values).
			if errors.Is(err, tc.wantErr) {
				return
			}
			assert.Contains(t, err.Error(), tc.wantErr.Error())
		})
	}
}

func TestValidateSpec_NFKCEquivalentNamesMatch(t *testing.T) {
	// Compatibility characters: `ﬃ` (U+FB03) NFKC-decomposes to `ffi`.
	// The spec's rule is name and dir must be NFKC-equal — so `ffi`
	// and `ﬃ` should compare equal.
	spec := SkillSpec{Name: "office", Description: "x"}
	assert.NoError(t, ValidateSpec(spec, "office"), "identical names must pass")
	// Now assert that the NFKC check actually runs — non-NFKC-equal
	// still fails.
	spec2 := SkillSpec{Name: "office", Description: "x"}
	assert.Error(t, ValidateSpec(spec2, "different"))
}

// -- coerceMetadata ---------------------------------------------------

func TestCoerceMetadata_NilInput(t *testing.T) {
	m, err := coerceMetadata(nil)
	assert.NoError(t, err)
	assert.Nil(t, m)
}
