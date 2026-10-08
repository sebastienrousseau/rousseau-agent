package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// isolatePGEnv blanks the libpq environment and home directory so
// pgconn's defaults cannot leak into the expected rendering. Values
// are set, never read or printed.
func isolatePGEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{
		"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD",
		"PGPASSFILE", "PGSERVICE", "PGSERVICEFILE", "PGSSLMODE",
		"PGSSLPASSWORD", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY",
	} {
		t.Setenv(k, "")
	}
}

// All passwords below are fakes.
func TestRedactDSN_Forms(t *testing.T) {
	isolatePGEnv(t)
	cases := []struct {
		name, in, want, secret string
	}{
		{"url", "postgres://alice:FAKEpw1@db.example:6543/app", "alice@db.example:6543/app", "FAKEpw1"},
		{"url-postgresql", "postgresql://alice:FAKEpw1@db.example/app", "alice@db.example:5432/app", "FAKEpw1"},
		{"url-no-password", "postgres://alice@db.example/app", "alice@db.example:5432/app", ""},
		{"keyword", "host=db.example user=alice password=FAKEpw2 dbname=app", "alice@db.example:5432/app", "FAKEpw2"},
		{"keyword-quoted", "host=db.example user=alice password='FAKE pw3' dbname=app", "alice@db.example:5432/app", "FAKE pw3"},
		{"query-password", "postgres://alice@db.example/app?password=FAKEpw4", "alice@db.example:5432/app", "FAKEpw4"},
		{"query-sslpassword", "postgres://alice@db.example/app?sslmode=disable&sslpassword=FAKEpw5", "alice@db.example:5432/app", "FAKEpw5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDSN(tc.in)
			assert.Equal(t, tc.want, got)
			if tc.secret != "" {
				assert.NotContains(t, got, tc.secret)
			}
		})
	}
}

func TestRedactDSN_UnparseableFallsBackToMasking(t *testing.T) {
	isolatePGEnv(t)
	cases := []struct {
		name, in, want string
	}{
		{"bad-port-url", "postgres://alice:FAKEpw6@db.example:notaport/app?sslpassword=FAKEpw7",
			"postgres://alice:***@db.example:notaport/app?sslpassword=***"},
		{"bad-port-keyword", "host=db.example port=notaport user=alice password=FAKEpw8",
			"host=db.example port=notaport user=alice password=***"},
		{"bad-port-query", "postgres://alice@db.example:notaport/app?PASSWORD=FAKEpw9&x=1",
			"postgres://alice@db.example:notaport/app?PASSWORD=***&x=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDSN(tc.in)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "FAKEpw")
		})
	}
}

func TestRedactDSN_SQLitePathUnchanged(t *testing.T) {
	isolatePGEnv(t)
	for _, p := range []string{"/var/lib/rousseau/sessions.db", "sessions.db", "file:sessions.db?cache=shared"} {
		assert.Equal(t, p, redactDSN(p))
	}
}
