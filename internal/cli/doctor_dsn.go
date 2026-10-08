package cli

import (
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// dsnKeywordForm spots a libpq keyword/value DSN ("host=x user=y").
	dsnKeywordForm = regexp.MustCompile(`^\s*[A-Za-z_]+\s*=`)
	// dsnPasswordParam masks password / sslpassword in keyword form
	// and in a URL query string, quoted values included.
	dsnPasswordParam = regexp.MustCompile(`(?i)\b((?:ssl)?password\s*=\s*)('(?:[^'\\]|\\.)*'|[^\s&]+)`)
	// dsnUserinfoPassword masks the password in scheme://user:pw@.
	dsnUserinfoPassword = regexp.MustCompile(`(://[^\s:/?#@]*):\S*@`)
)

// redactDSN renders a Postgres DSN for diagnostic output without its
// credentials. A DSN pgconn can parse, in URL or keyword form, is
// rendered as user@host:port/database; one it cannot parse has every
// password it carries masked instead. Anything that does not look like
// a Postgres DSN (a SQLite path) passes through unchanged.
func redactDSN(dsn string) string {
	if !looksLikePostgresDSN(dsn) {
		return dsn
	}
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return maskDSNPasswords(dsn)
	}
	hostPort := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	return cfg.User + "@" + hostPort + "/" + cfg.Database
}

func looksLikePostgresDSN(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return true
	}
	if strings.Contains(dsn, "://") {
		return true // some other URL: mask it rather than trust it
	}
	return dsnKeywordForm.MatchString(dsn)
}

// maskDSNPasswords is the fallback for a DSN pgconn rejects.
func maskDSNPasswords(dsn string) string {
	out := dsnPasswordParam.ReplaceAllString(dsn, "${1}***")
	return dsnUserinfoPassword.ReplaceAllString(out, "${1}:***@")
}
