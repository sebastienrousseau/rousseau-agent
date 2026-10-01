// Package health gives a running transport daemon an out-of-process
// liveness signal. The daemon rewrites a small heartbeat file on a
// fixed interval; `rousseau health <transport>` (wired as the
// container's HealthCmd) fails when that file is missing, stale, or
// reports the transport disconnected.
//
// It exists because "process running" is not "bridge working": the
// 2026-09-27 outage left a WhatsApp daemon up for three days with no
// session, and systemd reported it healthy throughout.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Beat is one heartbeat record.
type Beat struct {
	Transport string    `json:"transport"`
	UpdatedAt time.Time `json:"updated_at"`
	PID       int       `json:"pid"`
	// Connected is nil for transports with no connection notion; for
	// the rest it is the transport's own view of its upstream link.
	Connected *bool `json:"connected,omitempty"`
}

// ConnectedFunc reports a transport's link state; ok=false means the
// transport has no such notion.
type ConnectedFunc func() (connected, ok bool)

// Path is the heartbeat file for transport under dataDir
// (normally $XDG_DATA_HOME/rousseau).
func Path(dataDir, transport string) string {
	return filepath.Join(dataDir, "health", transport+".json")
}

// Write records one beat atomically (temp file + rename), so a reader
// never sees a half-written file.
func Write(path string, b Beat) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("health: dir: %w", err)
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("health: write: %w", err)
	}
	return os.Rename(tmp, path)
}

// Run writes a beat for transport every interval until ctx ends. conn
// may be nil.
func Run(ctx context.Context, path, transport string, conn ConnectedFunc, interval time.Duration) {
	beat := func() {
		b := Beat{Transport: transport, UpdatedAt: time.Now().UTC(), PID: os.Getpid()}
		if conn != nil {
			if c, ok := conn(); ok {
				b.Connected = &c
			}
		}
		_ = Write(path, b) //nolint:errcheck // a missed beat shows up as staleness, which is the signal
	}
	beat()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			beat()
		}
	}
}

// Check reads the heartbeat at path and returns nil only when it was
// written within maxAge and, if the transport reports a link, that
// link is up.
func Check(path string, maxAge time.Duration, now time.Time) error {
	data, err := os.ReadFile(path) //nolint:gosec // path is built by Path from config, not user input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("no heartbeat: the daemon is not running or has not started its transport")
		}
		return fmt.Errorf("read heartbeat: %w", err)
	}
	var b Beat
	if err := json.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("malformed heartbeat: %w", err)
	}
	if age := now.Sub(b.UpdatedAt); age > maxAge {
		return fmt.Errorf("stale heartbeat: last written %s ago (limit %s)", age.Round(time.Second), maxAge)
	}
	if b.Connected != nil && !*b.Connected {
		return fmt.Errorf("%s is not connected upstream", b.Transport)
	}
	return nil
}
