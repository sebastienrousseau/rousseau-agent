package cli

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/model"
	pgstore "github.com/sebastienrousseau/rousseau-agent/internal/state/postgres"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// recallStores opens a SQLite store and, when ROUSSEAU_TEST_POSTGRES_URL
// is set, a Postgres store in a throwaway schema.
func recallStores(t *testing.T) map[string]SearchableStore {
	t.Helper()
	ctx := context.Background()
	lite, err := sqlitestore.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = lite.Close() }) //nolint:errcheck // test cleanup
	out := map[string]SearchableStore{"sqlite": lite}
	base := os.Getenv("ROUSSEAU_TEST_POSTGRES_URL")
	if base == "" {
		return out
	}
	schema := "recall_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	db, err := sql.Open("pgx", base)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) //nolint:errcheck // test cleanup
		_ = db.Close()                                                                //nolint:errcheck // test cleanup
	})
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	pg, err := pgstore.Open(ctx, base+sep+"search_path="+schema)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Close() }) //nolint:errcheck // test cleanup
	out["postgres"] = pg
	return out
}

// L-12: recall must survive user text that is FTS5 syntax (an
// apostrophe, a hyphen, an operator word, NEAR( ...). Before the fix
// the raw words reached MATCH, the query failed and recall silently
// returned nothing.
func TestRecall_FTSSyntaxInUserTextStillRecalls(t *testing.T) {
	for name, st := range recallStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			require.NoError(t, st.EnsureSearch(ctx))
			past := model.NewSession("old chat")
			past.Sender = "telegram:alice"
			past.Append(model.NewUserText("remember the mailbox quota for the e-mail server"))
			require.NoError(t, st.Save(ctx, past))

			p := buildRecallProvider(st)
			for _, text := range []string{
				"why don't mailbox quotas apply",
				"mailbox e-mail quota",
				"mailbox AND NOT quota",
				"NEAR(mailbox quota",
				`mailbox say"hi quota`,
			} {
				cur := agent.NewSession("now")
				cur.Sender = "telegram:alice"
				cur.Append(agent.NewUserText(text))
				got := p.SystemAppendix(ctx, cur)
				assert.Contains(t, got, "mailbox quota", "user text %q", text)
			}
		})
	}
}
