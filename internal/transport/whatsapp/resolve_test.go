package whatsapp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func jid(user string, device uint16) types.JID {
	return types.JID{User: user, Server: "s.whatsapp.net", Device: device}
}

func lidJID(hash string) types.JID {
	return types.JID{User: hash, Server: "lid"}
}

func msgEvent(sender types.JID, chat types.JID, isFromMe, isGroup bool, body string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender:   sender,
				Chat:     chat,
				IsFromMe: isFromMe,
				IsGroup:  isGroup,
			},
			Timestamp: time.Unix(1_700_000_000, 0),
		},
		Message: &waProto.Message{Conversation: proto.String(body)},
	}
}

func TestResolveInbound_HappyPathFromContact(t *testing.T) {
	own := jid("447906009073", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hello")

	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "15551234567@s.whatsapp.net", res.Msg.From)
	assert.Equal(t, "hello", res.Msg.Body)
}

func TestResolveInbound_GroupIsSkipped(t *testing.T) {
	own := jid("447906009073", 21)
	evt := msgEvent(jid("15551234567", 0), types.JID{User: "gid", Server: "g.us"}, false, true, "hi")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipGroup, res.Skip)
}

func TestResolveInbound_OwnDeviceEchoIsSkipped(t *testing.T) {
	own := jid("447906009073", 21)
	// Same device — this is our own outbound echoing back.
	evt := msgEvent(own, own.ToNonAD(), true, false, "reply we sent")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipOwnDevice, res.Skip)
}

func TestResolveInbound_OtherLinkedDeviceIsProcessed(t *testing.T) {
	own := jid("447906009073", 21)
	// Same account, different device — this is "message yourself" from
	// the primary phone. Must NOT be filtered by IsFromMe.
	phone := jid("447906009073", 0)
	evt := msgEvent(phone, phone.ToNonAD(), true, false, "hi from phone")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "447906009073@s.whatsapp.net", res.Msg.From)
}

func TestResolveInbound_LIDSubstitutedToAccountJID(t *testing.T) {
	own := jid("447906009073", 21)
	// Newer WhatsApp reports the account holder's outbound sender as a
	// LID — substitute the account JID so allowlists match.
	lid := lidJID("276540210315282")
	evt := msgEvent(lid, own.ToNonAD(), true, false, "self-chat via lid")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "447906009073@s.whatsapp.net", res.Msg.From,
		"LID sender should be rewritten to the account JID")
}

func TestResolveInbound_MultiDeviceSuffixStripped(t *testing.T) {
	own := jid("447906009073", 21)
	// Contact sender with a device suffix — allowlist should still
	// match on the plain phone-number JID.
	sender := jid("15551234567", 3)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	res := ResolveInbound(evt, &own)
	require.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "15551234567@s.whatsapp.net", res.Msg.From)
}

func TestResolveInbound_EmptyBodyIsSkipped(t *testing.T) {
	own := jid("447906009073", 21)
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: jid("15551234567", 0),
				Chat:   jid("15551234567", 0).ToNonAD(),
			},
		},
		Message: &waProto.Message{},
	}
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipEmptyText, res.Skip)
}

func TestResolveInbound_NilEventOrMessage(t *testing.T) {
	assert.Equal(t, SkipEmptyText, ResolveInbound(nil, nil).Skip)
	assert.Equal(t, SkipEmptyText, ResolveInbound(&events.Message{}, nil).Skip)
}

func TestResolveInbound_ExtendedTextMessageIsRead(t *testing.T) {
	own := jid("447906009073", 21)
	sender := jid("15551234567", 0)
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Sender: sender, Chat: sender.ToNonAD()},
			Timestamp:     time.Unix(0, 0),
		},
		Message: &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("quoted reply body")},
		},
	}
	res := ResolveInbound(evt, &own)
	require.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "quoted reply body", res.Msg.Body)
}

func TestPrependHeader_DefaultWhenEmpty(t *testing.T) {
	got := PrependHeader("hi", "")
	assert.Equal(t, DefaultReplyHeader+"hi", got)
}

func TestPrependHeader_ExplicitOverride(t *testing.T) {
	got := PrependHeader("hi", "🤖 *Bot*\n\n")
	assert.Equal(t, "🤖 *Bot*\n\nhi", got)
}

func TestPrependHeader_SingleSpaceDisables(t *testing.T) {
	got := PrependHeader("hi", " ")
	assert.Equal(t, "hi", got)
}
