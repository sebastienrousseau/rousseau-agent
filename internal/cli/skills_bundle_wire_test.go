package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
	"github.com/sebastienrousseau/rousseau-agent/internal/skills/bundle"
)

// writeSignedBundleAndGetTrustKey drops one signed bundle into
// dir and returns the base64 std-encoded public key an operator
// would paste into agent.skill_bundles.trusted_publisher_keys.
func writeSignedBundleAndGetTrustKey(t *testing.T, dir, name string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	b := &bundle.Bundle{
		Manifest: bundle.Manifest{
			Name: name, Version: "1.0.0", Publisher: "test",
			PublishedAt: time.Now().UTC().Format(time.RFC3339),
			Triggers:    []string{name},
		},
		Content: "body of " + name,
	}
	b.PopulateHashes()
	b.Signature = bundle.Sign(b.Manifest, priv)
	data, err := json.Marshal(b)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".skill.json"), data, 0o600))
	return base64.StdEncoding.EncodeToString(pub)
}

func TestBuildSkillsProvider_BundlesIgnoredWithoutLicence(t *testing.T) {
	// Load-bearing: unlicensed operator who configures
	// bundle_dir + trusted_publisher_keys still gets a
	// provider (skills_dir may be empty) but the bundles
	// themselves are NOT loaded. No silent activation.
	dir := t.TempDir()
	_ = writeSignedBundleAndGetTrustKey(t, dir, "kept")

	opts := &Options{Config: &config.Config{
		Agent: config.AgentConfig{
			SkillBundles: config.SkillBundlesConfig{
				Dir: dir,
				// TrustedPublisherKeys deliberately empty
				// because even a valid list can't unlock
				// unlicensed daemon.
			},
		},
	}, Logger: silentLogger()}
	p, err := buildSkillsProvider(opts, license.Core())
	require.NoError(t, err)
	require.NotNil(t, p)
	// With dir set but bundles gated, the provider carries
	// zero skills.
	assert.Empty(t, p.(interface{ Skills() []skills.Skill }).Skills())
}

func TestBuildSkillsProvider_BundlesLoadWhenLicensed(t *testing.T) {
	dir := t.TempDir()
	pubB64 := writeSignedBundleAndGetTrustKey(t, dir, "trusted")

	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	opts := &Options{Config: &config.Config{
		Agent: config.AgentConfig{
			SkillBundles: config.SkillBundlesConfig{
				Dir:                  dir,
				TrustedPublisherKeys: []string{pubB64},
			},
		},
	}, Logger: silentLogger()}
	p, err := buildSkillsProvider(opts, chk)
	require.NoError(t, err)
	require.NotNil(t, p)
	loaded := p.(interface{ Skills() []skills.Skill }).Skills()
	require.Len(t, loaded, 1)
	assert.Equal(t, "trusted", loaded[0].Name)
}

func TestBuildSkillsProvider_BundlesRejectedWhenTrustListEmpty(t *testing.T) {
	// Licensed but no trusted keys → refuse to load. A
	// bundle dir without a trust root is a misconfiguration
	// worth surfacing (WARN log) not silently accepting.
	dir := t.TempDir()
	_ = writeSignedBundleAndGetTrustKey(t, dir, "spooky")

	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	opts := &Options{Config: &config.Config{
		Agent: config.AgentConfig{
			SkillBundles: config.SkillBundlesConfig{Dir: dir},
		},
	}, Logger: silentLogger()}
	p, err := buildSkillsProvider(opts, chk)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Empty(t, p.(interface{ Skills() []skills.Skill }).Skills(), "empty trust list must not silently accept bundles")
}

func TestBuildSkillsProvider_BundlesRejectedWhenTrustListMalformed(t *testing.T) {
	// A trust-list entry that isn't decodable base64 (or is
	// the wrong length) must fail-safe: WARN + no bundles.
	// Never crash on operator typos.
	dir := t.TempDir()
	_ = writeSignedBundleAndGetTrustKey(t, dir, "bundle")

	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	opts := &Options{Config: &config.Config{
		Agent: config.AgentConfig{
			SkillBundles: config.SkillBundlesConfig{
				Dir:                  dir,
				TrustedPublisherKeys: []string{"not-real-base64!!!"},
			},
		},
	}, Logger: silentLogger()}
	p, err := buildSkillsProvider(opts, chk)
	require.NoError(t, err, "malformed trust list must not error at boot")
	require.NotNil(t, p)
	assert.Empty(t, p.(interface{ Skills() []skills.Skill }).Skills())
}

func TestBuildSkillsProvider_PlainAndBundlesCompose(t *testing.T) {
	// Property: plain markdown skills and verified bundles
	// combine into one Provider. Operator gets both sources
	// without a config-side choice.
	plainDir := t.TempDir()
	writeSkill(t, plainDir, "plain-skill", "a plain markdown skill")

	bundleDir := t.TempDir()
	pubB64 := writeSignedBundleAndGetTrustKey(t, bundleDir, "bundle-skill")

	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	opts := &Options{Config: &config.Config{
		Agent: config.AgentConfig{
			SkillsDir: plainDir,
			SkillBundles: config.SkillBundlesConfig{
				Dir:                  bundleDir,
				TrustedPublisherKeys: []string{pubB64},
			},
		},
	}, Logger: silentLogger()}
	p, err := buildSkillsProvider(opts, chk)
	require.NoError(t, err)
	require.NotNil(t, p)
	names := map[string]bool{}
	for _, s := range p.(interface{ Skills() []skills.Skill }).Skills() {
		names[s.Name] = true
	}
	assert.True(t, names["plain-skill"], "plain skill missing")
	assert.True(t, names["bundle-skill"], "bundle skill missing")
}

// -- doctor rows --

func TestCheckGovernance_SkillBundlesRowsSurfaceWhenConfigured(t *testing.T) {
	got := checkGovernance(&config.Config{Agent: config.AgentConfig{
		SkillBundles: config.SkillBundlesConfig{
			Dir:                  "/etc/rousseau/bundles",
			TrustedPublisherKeys: []string{"key1", "key2"},
		},
	}}, license.Core())

	var haveDir, haveKeys, haveLicensed diagResult
	for _, r := range got {
		switch r.Name {
		case "identity.governance.skill_bundles.dir":
			haveDir = r
		case "identity.governance.skill_bundles.trusted_publishers":
			haveKeys = r
		case "identity.governance.skill_bundles.licensed":
			haveLicensed = r
		}
	}
	assert.Equal(t, "/etc/rousseau/bundles", haveDir.Detail)
	assert.Contains(t, haveKeys.Detail, "2 key")
	assert.Equal(t, "warn", haveLicensed.Status)
}

func TestCheckGovernance_SkillBundlesLicensedReportsOK(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got := checkGovernance(&config.Config{Agent: config.AgentConfig{
		SkillBundles: config.SkillBundlesConfig{
			Dir:                  "/etc/rousseau/bundles",
			TrustedPublisherKeys: []string{"key1"},
		},
	}}, chk)
	var status string
	for _, r := range got {
		if r.Name == "identity.governance.skill_bundles.licensed" {
			status = r.Status
		}
	}
	assert.Equal(t, "ok", status)
}
