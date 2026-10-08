package matrix

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"
)

// directTTL is how long a room's one-to-one verdict is reused before
// joined_members is asked again, so a DM that gains a third member is
// noticed within that window.
const directTTL = 10 * time.Minute

// directCache remembers, per room, whether it had exactly two joined
// members. The zero value is ready to use.
type directCache struct {
	mu    sync.Mutex
	rooms map[string]directEntry
}

type directEntry struct {
	direct  bool
	expires time.Time
}

func (d *directCache) get(room string, now time.Time) (direct, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, found := d.rooms[room]
	if !found || now.After(e.expires) {
		return false, false
	}
	return e.direct, true
}

func (d *directCache) put(room string, direct bool, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rooms == nil {
		d.rooms = make(map[string]directEntry)
	}
	d.rooms[room] = directEntry{direct: direct, expires: now.Add(directTTL)}
}

// isDirectRoom reports whether roomID is a one-to-one room: exactly
// two joined members, the bot and one other. A lookup failure counts
// as not direct (the safe default) and is not cached, so the next
// message retries.
func (c *Client) isDirectRoom(ctx context.Context, roomID string) bool {
	now := time.Now()
	if direct, ok := c.direct.get(roomID, now); ok {
		return direct
	}
	var resp struct {
		Joined map[string]struct{} `json:"joined"`
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/joined_members",
		c.cfg.HomeserverURL, url.PathEscape(roomID))
	if err := c.doGET(ctx, endpoint, &resp); err != nil {
		c.logger.Warn("matrix.joined_members_failed",
			slog.String("room", roomID),
			slog.String("err", err.Error()))
		return false
	}
	direct := len(resp.Joined) == 2
	c.direct.put(roomID, direct, now)
	return direct
}
