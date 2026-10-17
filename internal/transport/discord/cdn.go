package discord

import (
	"regexp"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Discord serves message attachments from these hosts only. The URL
// arrives in the gateway payload, so the daemon checks it before
// fetching instead of GETting whatever it is handed.
var discordCDNHosts = []string{"cdn.discordapp.com", "media.discordapp.net"}

// cdnPolicy decides which attachment URLs may be fetched: the https
// scheme and exactly one Discord CDN host (no port, no userinfo, no
// look-alike suffix). It is a field on Client so in-package tests can
// stand an httptest server in for the CDN.
type cdnPolicy struct {
	pattern *regexp.Regexp
}

func defaultCDNPolicy() cdnPolicy {
	return cdnPolicy{pattern: transport.FetchPattern("https", discordCDNHosts...)}
}
