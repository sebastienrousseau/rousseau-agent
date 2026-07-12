package whatsapp

import (
	"strings"
	"testing"

	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// FuzzResolveInbound feeds arbitrary strings into every routing-
// relevant field. The invariant: ResolveInbound must never panic and
// must always return a Resolved value.
func FuzzResolveInbound(f *testing.F) {
	seeds := []struct {
		user, server, chat, body string
		isFromMe, isGroup        bool
	}{
		{"447906009073", "s.whatsapp.net", "447906009073@s.whatsapp.net", "hi", true, false},
		{"276540210315282", "lid", "447906009073@s.whatsapp.net", "self via lid", true, false},
		{"15551234567", "s.whatsapp.net", "15551234567@s.whatsapp.net", "hello", false, false},
		{"gid", "g.us", "gid@g.us", "group ignored", false, true},
		{"", "", "", "", false, false},
	}
	for _, s := range seeds {
		f.Add(s.user, s.server, s.chat, s.body, s.isFromMe, s.isGroup)
	}

	own := types.JID{User: "447906009073", Server: "s.whatsapp.net", Device: 21}

	f.Fuzz(func(t *testing.T, user, server, chatStr, body string, isFromMe, isGroup bool) {
		// Skip inputs likely to trip the JID parser itself; the fuzzer
		// is looking for bugs in our routing, not upstream's.
		if strings.ContainsRune(user, '@') || strings.ContainsRune(server, '@') {
			t.Skip()
		}
		sender := types.JID{User: user, Server: server}
		chat, err := types.ParseJID(chatStr)
		if err != nil {
			chat = sender
		}
		evt := &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{
					Sender: sender, Chat: chat,
					IsFromMe: isFromMe, IsGroup: isGroup,
				},
			},
			Message: &waProto.Message{Conversation: proto.String(body)},
		}
		res := ResolveInbound(evt, &own)
		// Invariant: If not skipped, Msg.Body must be non-empty and
		// From must contain "@".
		if res.Skip == SkipNone {
			if res.Msg.Body == "" {
				t.Fatalf("SkipNone with empty body")
			}
			if !strings.Contains(res.Msg.From, "@") {
				t.Fatalf("SkipNone with malformed From: %q", res.Msg.From)
			}
		}
	})
}
