package cli

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
)

// Regression tests for the SkillsMode toggle wiring — the Phase 2.1
// daemon-assembly hook that lets operators pick the spec-compliant
// three-tier loader over the legacy flat-file model.

// -- skillsMode enum --------------------------------------------------

func TestSkillsMode_ValuesMap(t *testing.T) {
	cases := map[string]skillsModeValue{
		"":       skillsModeLegacy, // zero-value → default → legacy
		"legacy": skillsModeLegacy,
		"LEGACY": skillsModeLegacy, // case-insensitive
		"spec":   skillsModeSpec,
		"SPEC":   skillsModeSpec,
		" spec ": skillsModeSpec,   //nolint:gocritic // deliberate: freezes whitespace-tolerance contract
		"junk":   skillsModeLegacy, // unknown → default → legacy
	}
	for input, want := range cases {
		t.Run("mode="+input, func(t *testing.T) {
			assert.Equal(t, want, skillsMode(input))
		})
	}
}

// -- Legacy mode preservation (baseline) ------------------------------

// The mode toggle must be additive — an unset SkillsMode must
// produce identical behaviour to the pre-Phase-2.1 loader.
func TestBuildSkillsProvider_LegacyModeUnchanged(t *testing.T) {
	dir := t.TempDir()
	// Write a legacy flat-file skill.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "git-rebase.md"),
		[]byte("---\nname: git-rebase\ndescription: Guide the user through a rebase.\ntriggers: [rebase, squash]\n---\nBody.\n"),
		0o600,
	))
	opts := &Options{
		Config: &config.Config{
			Agent: config.AgentConfig{
				SkillsDir: dir,
				// SkillsMode unset — legacy path.
			},
		},
	}
	p, err := buildSkillsProvider(opts, license.Core())
	require.NoError(t, err)
	require.NotNil(t, p)
	legacy, ok := p.(*skills.Provider)
	require.True(t, ok, "unset SkillsMode must return the legacy skills.Provider")
	require.Len(t, legacy.Skills(), 1)
	assert.Equal(t, "git-rebase", legacy.Skills()[0].Name)
}

// -- Spec-mode wiring -------------------------------------------------

func TestBuildSkillsProvider_SpecModeWiresSpecProvider(t *testing.T) {
	dir := t.TempDir()
	// Layout: dir/greet/SKILL.md — the spec-compliant per-skill
	// subdirectory shape, NOT flat *.md.
	skillDir := filepath.Join(dir, "greet")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: greet\ndescription: Greet the user warmly.\n---\nHi!\n"),
		0o600,
	))
	opts := &Options{
		Config: &config.Config{
			Agent: config.AgentConfig{
				SkillsDir:  dir,
				SkillsMode: "spec",
			},
		},
	}
	p, err := buildSkillsProvider(opts, license.Core())
	require.NoError(t, err)
	require.NotNil(t, p)
	spec, ok := p.(*skills.SpecProvider)
	require.True(t, ok, "SkillsMode=spec must return the spec-compliant SpecProvider")
	specSkills := spec.Skills()
	require.Len(t, specSkills, 1)
	assert.Equal(t, "greet", specSkills[0].Name)
	assert.Equal(t, "Greet the user warmly.", specSkills[0].Description)
}

func TestBuildSkillsProvider_SpecModeIgnoresLegacyFlatFiles(t *testing.T) {
	// A flat *.md file in the SkillsDir root (legacy shape) MUST
	// NOT be discovered by the spec walker — spec-mode looks for
	// subdirectories with SKILL.md, and legacy flat files should
	// be either invisible or explicit failures rather than silently
	// mis-parsed.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "legacy-thing.md"),
		[]byte("---\nname: legacy-thing\ndescription: Should not appear.\ntriggers: [x]\n---\n"),
		0o600,
	))
	opts := &Options{
		Config: &config.Config{
			Agent: config.AgentConfig{
				SkillsDir:  dir,
				SkillsMode: "spec",
			},
		},
	}
	p, err := buildSkillsProvider(opts, license.Core())
	require.NoError(t, err)
	spec, ok := p.(*skills.SpecProvider)
	require.True(t, ok)
	assert.Empty(t, spec.Skills(),
		"spec-mode must ignore flat legacy files at the root — they need to be moved into per-skill subdirectories")
}

func TestBuildSkillsProvider_SpecModeMissingDirIsNoOp(t *testing.T) {
	opts := &Options{
		Config: &config.Config{
			Agent: config.AgentConfig{
				SkillsDir:  "/definitely/does/not/exist/rousseau-spec-tests",
				SkillsMode: "spec",
			},
		},
	}
	p, err := buildSkillsProvider(opts, license.Core())
	require.NoError(t, err)
	require.NotNil(t, p, "spec-mode with a missing dir must still return an empty provider, not nil")
	spec, ok := p.(*skills.SpecProvider)
	require.True(t, ok)
	assert.Empty(t, spec.Skills())
}

// -- Spec-mode + bundles interaction ---------------------------------

// Spec mode must warn when the operator has SkillBundles configured
// (a legacy-only concept) so they know their bundles aren't loaded.
// The daemon still starts — refusing to boot on this misconfig
// would be too disruptive for a warning-only situation.
func TestBuildSkillsProvider_SpecModeWarnsOnBundleConfig(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "greet")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: greet\ndescription: Greet.\n---\n"),
		0o600,
	))

	// Capture log output.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	opts := &Options{
		Logger: logger,
		Config: &config.Config{
			Agent: config.AgentConfig{
				SkillsDir:  dir,
				SkillsMode: "spec",
				SkillBundles: config.SkillBundlesConfig{
					Dir: "/some/bundles/dir",
				},
			},
		},
	}
	p, err := buildSkillsProvider(opts, license.Core())
	require.NoError(t, err)
	require.NotNil(t, p)
	_, ok := p.(*skills.SpecProvider)
	require.True(t, ok, "spec-mode still returns SpecProvider even with bundles configured")

	logs := buf.String()
	assert.Contains(t, logs, "skills.spec_ignores_bundles",
		"the WARN event must fire so operators see 'your bundles are inert in spec mode'")
	assert.Contains(t, logs, "/some/bundles/dir",
		"the offending path must be in the log for correlatable diagnostics")
	assert.Contains(t, logs, "x-rousseau-signature",
		"the log must point at the planned spec-mode signing path")
}

// -- No-source-configured baseline (both modes) -----------------------

// The (nil, nil) return contract when NEITHER dir is set must hold
// in both modes — the daemon wiring uses nil to decide whether to
// plumb the SkillsProvider through agent.Options at all.
func TestBuildSkillsProvider_NoSourceReturnsNilNil_Legacy(t *testing.T) {
	noHome(t) // strip HOME so resolveSkillsDir returns ""
	opts := &Options{Config: &config.Config{}}
	p, err := buildSkillsProvider(opts, license.Core())
	assert.NoError(t, err)
	assert.Nil(t, p, "no SkillsDir + no SkillBundles must return (nil, nil) in legacy mode")
}

func TestBuildSkillsProvider_NoSourceReturnsNilNil_Spec(t *testing.T) {
	noHome(t)
	opts := &Options{
		Config: &config.Config{
			Agent: config.AgentConfig{SkillsMode: "spec"},
		},
	}
	p, err := buildSkillsProvider(opts, license.Core())
	assert.NoError(t, err)
	assert.Nil(t, p, "no SkillsDir + no SkillBundles must return (nil, nil) in spec mode too")
}

// -- Spec-mode + bundles-only edge case ------------------------------

// A rare but real config: spec mode enabled, no SkillsDir, but
// SkillBundles.Dir is set. buildSpecSkillsProvider must warn about
// the ignored bundles AND return a valid empty SpecProvider (not
// nil), so downstream wiring's "nil = no skills, skip Options plumb"
// contract from the (nil, nil) case doesn't mis-fire when SOMETHING
// is configured.
func TestBuildSkillsProvider_SpecModeBundlesOnlyReturnsEmptyProvider(t *testing.T) {
	noHome(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	opts := &Options{
		Logger: logger,
		Config: &config.Config{
			Agent: config.AgentConfig{
				SkillsMode: "spec",
				SkillBundles: config.SkillBundlesConfig{
					Dir: "/some/bundles/dir",
				},
			},
		},
	}
	p, err := buildSkillsProvider(opts, license.Core())
	require.NoError(t, err)
	require.NotNil(t, p, "spec-mode with a bundle config must still return a provider")
	spec, ok := p.(*skills.SpecProvider)
	require.True(t, ok)
	assert.Empty(t, spec.Skills(), "no SkillsDir means empty provider")
	assert.Contains(t, buf.String(), "skills.spec_ignores_bundles")
}

// -- Spec-mode info log ----------------------------------------------

// The successful spec-mode load emits a single INFO log with the
// discovered count so operators can verify from a rousseau doctor
// or systemd journal snapshot that their skills registered.
func TestBuildSkillsProvider_SpecModeLogsDiscoveredCount(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		sd := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(sd, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(sd, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: A skill.\n---\n"),
			0o600,
		))
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	opts := &Options{
		Logger: logger,
		Config: &config.Config{
			Agent: config.AgentConfig{SkillsDir: dir, SkillsMode: "spec"},
		},
	}
	_, err := buildSkillsProvider(opts, license.Core())
	require.NoError(t, err)

	logs := buf.String()
	assert.Contains(t, logs, "skills.spec_mode")
	assert.True(t,
		strings.Contains(logs, "discovered=3") || strings.Contains(logs, `"discovered":3`),
		"spec-mode log must include the discovered count (got: %s)", logs)
}
