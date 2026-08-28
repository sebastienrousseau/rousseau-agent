//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// tempStoreDSN returns a sqlite DSN pointing at a throwaway file in
// t.TempDir(). modernc.org/sqlite is pure Go, so this touches nothing
// beyond the local filesystem.
func tempStoreDSN(t *testing.T) string {
	t.Helper()
	return "file:" + filepath.Join(t.TempDir(), "wa.db") + "?_pragma=foreign_keys(1)"
}

// unpairedDevice builds a real (never-connected, never-paired)
// whatsmeow device backed by a temp sqlite store.
func unpairedDevice(t *testing.T) *store.Device {
	t.Helper()
	container, err := sqlstore.New(context.Background(), "sqlite", tempStoreDSN(t), waLog.Noop)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Close() }) //nolint:errcheck // test setup/teardown
	device, err := container.GetFirstDevice(context.Background())
	require.NoError(t, err)
	return device
}

func TestWMSender_NotLoggedInSurfacesError(t *testing.T) {
	wm := whatsmeow.NewClient(unpairedDevice(t), waLog.Noop)
	t.Cleanup(wm.Disconnect)
	s := newWMSender(wm)
	require.NotNil(t, s)

	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	assert.Error(t, s.SendText(context.Background(), chat, "hi"))
	assert.ErrorIs(t, s.SendPresence(context.Background(), chat, types.ChatPresenceComposing), whatsmeow.ErrNotLoggedIn)
}

func TestWMDownloader_NoURLPresent(t *testing.T) {
	wm := whatsmeow.NewClient(unpairedDevice(t), waLog.Noop)
	t.Cleanup(wm.Disconnect)
	d := newWMDownloader(wm)
	audio := &waProto.AudioMessage{Mimetype: proto.String("audio/ogg")}
	b, mime, err := d.Download(context.Background(), audio)
	assert.Error(t, err)
	assert.Nil(t, b)
	assert.Equal(t, "audio/ogg", mime)
}

type notDownloadable struct{}

func (notDownloadable) GetMimetype() string { return "audio/ogg" }
func (notDownloadable) GetSeconds() uint32  { return 1 }

func TestWMDownloader_RejectsNonWhatsmeowMessage(t *testing.T) {
	wm := whatsmeow.NewClient(unpairedDevice(t), waLog.Noop)
	t.Cleanup(wm.Disconnect)
	d := newWMDownloader(wm)
	_, _, err := d.Download(context.Background(), notDownloadable{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not whatsmeow.DownloadableMessage")
}

func TestParseJID_MalformedUserSurfacesParserError(t *testing.T) {
	// Two dots in the user part is the one shape whatsmeow's parser
	// rejects outright.
	_, err := parseJID("1.2.3@s.whatsapp.net")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whatsapp: parse JID")
}
