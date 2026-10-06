package envscrub

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScrub_DropsSecretsKeepsBaseline(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/x",
		"ANTHROPIC_API_KEY=sk-live",
		"ROUSSEAU_LICENSE_KEY=lic",
		"SLACK_BOT_TOKEN=xoxb",
		"LANG=C.UTF-8",
		"MALFORMED",
		"=novalue",
	}
	got := Scrub(in, nil)
	assert.Equal(t, []string{"HOME=/home/x", "LANG=C.UTF-8", "PATH=/usr/bin"}, got)
}

func TestScrub_ExtraNamesAndPrefixes(t *testing.T) {
	in := []string{"GIT_AUTHOR_NAME=a", "GIT_SSH_COMMAND=ssh", "GH_TOKEN=t", "PATH=/bin", "GITHUB_TOKEN=x"}
	got := Scrub(in, []string{"GH_TOKEN", " GIT_* ", ""})
	assert.Equal(t, []string{"GH_TOKEN=t", "GIT_AUTHOR_NAME=a", "GIT_SSH_COMMAND=ssh", "PATH=/bin"}, got)
}

func TestDefaultAllow_HasNoCredentialNames(t *testing.T) {
	for _, n := range DefaultAllow() {
		assert.NotContains(t, n, "KEY")
		assert.NotContains(t, n, "TOKEN")
		assert.NotContains(t, n, "SECRET")
	}
}
