package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// longSession is a 350-message conversation of ~1.2 KB messages, about
// the size of the largest live session (348 messages, 430 KB).
func longSession() *agent.Session {
	sess := agent.NewSession("long")
	for i := range 350 {
		role := agent.RoleUser
		if i%2 == 1 {
			role = agent.RoleAssistant
		}
		sess.Append(agent.Message{Role: role, Content: []agent.Content{{Kind: agent.ContentText,
			Text: strings.Repeat("a realistic sentence of conversation. ", 32)}}})
	}
	return sess
}

func walSize(b *testing.B, path string) int64 {
	b.Helper()
	info, err := os.Stat(path + "-wal")
	if err != nil {
		return 0
	}
	return info.Size()
}

// BenchmarkSave_AppendOneTurn measures one turn (one new message) on a
// 350-message session: the v2 append, and the v1 layout's whole-payload
// rewrite for comparison. wal-bytes/op is what the database wrote.
func BenchmarkSave_AppendOneTurn(b *testing.B) {
	ctx := context.Background()
	b.Run("v2-append", func(b *testing.B) {
		path := filepath.Join(b.TempDir(), "s.db")
		s, err := Open(ctx, path)
		if err != nil {
			b.Fatal(err)
		}
		defer s.Close() //nolint:errcheck // benchmark
		if _, err := s.db.ExecContext(ctx, `PRAGMA wal_autocheckpoint = 0`); err != nil {
			b.Fatal(err)
		}
		sess := longSession()
		if err := s.Save(ctx, sess); err != nil {
			b.Fatal(err)
		}
		before := walSize(b, path)
		b.ResetTimer()
		for range b.N {
			sess.Append(agent.NewUserText("one more turn"))
			if err := s.Save(ctx, sess); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(walSize(b, path)-before)/float64(b.N), "wal-bytes/op")
	})
	b.Run("v1-rewrite", func(b *testing.B) {
		path := filepath.Join(b.TempDir(), "s.db")
		s, err := Open(ctx, path)
		if err != nil {
			b.Fatal(err)
		}
		defer s.Close() //nolint:errcheck // benchmark
		if _, err := s.db.ExecContext(ctx, `PRAGMA wal_autocheckpoint = 0`); err != nil {
			b.Fatal(err)
		}
		sess := longSession()
		write := func() {
			payload, _ := json.Marshal(sess) //nolint:errcheck // fixture always marshals
			if _, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at, sender, search_text)
VALUES (?, ?, ?, ?, 'x', 'x', '', ?)
ON CONFLICT(id) DO UPDATE SET payload = excluded.payload, message_count = excluded.message_count,
    search_text = excluded.search_text`,
				sess.ID, sess.Title, string(payload), len(sess.Messages), searchText(sess)); err != nil {
				b.Fatal(err)
			}
		}
		write()
		before := walSize(b, path)
		b.ResetTimer()
		for range b.N {
			sess.Append(agent.NewUserText("one more turn"))
			write()
		}
		b.ReportMetric(float64(walSize(b, path)-before)/float64(b.N), "wal-bytes/op")
	})
}
