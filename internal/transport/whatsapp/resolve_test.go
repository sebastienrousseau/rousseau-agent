//go:build !no_whatsmeow

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

// msgEventWithAlt constructs an event where Sender arrives as an LID
// and SenderAlt carries the phone-number form — this is the shape
// WhatsApp emits when the sender has privacy features enabled.
func msgEventWithAlt(sender, alt, chat types.JID, isFromMe bool, body string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender:    sender,
				SenderAlt: alt,
				Chat:      chat,
				IsFromMe:  isFromMe,
			},
			Timestamp: time.Unix(1_700_000_000, 0),
		},
		Message: &waProto.Message{Conversation: proto.String(body)},
	}
}

func TestResolveInbound_HappyPathFromContact(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hello")

	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "15551234567@s.whatsapp.net", res.Msg.From)
	assert.Equal(t, "hello", res.Msg.Body)
}

func TestResolveInbound_GroupIsSkipped(t *testing.T) {
	own := jid("15551234567", 21)
	evt := msgEvent(jid("15551234567", 0), types.JID{User: "gid", Server: "g.us"}, false, true, "hi")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipGroup, res.Skip)
}

func TestResolveInbound_OwnDeviceEchoIsSkipped(t *testing.T) {
	own := jid("15551234567", 21)
	// Same device — this is our own outbound echoing back.
	evt := msgEvent(own, own.ToNonAD(), true, false, "reply we sent")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipOwnDevice, res.Skip)
}

func TestResolveInbound_OtherLinkedDeviceIsProcessed(t *testing.T) {
	own := jid("15551234567", 21)
	// Same account, different device — this is "message yourself" from
	// the primary phone. Must NOT be filtered by IsFromMe.
	phone := jid("15551234567", 0)
	evt := msgEvent(phone, phone.ToNonAD(), true, false, "hi from phone")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "15551234567@s.whatsapp.net", res.Msg.From)
}

func TestResolveInbound_IsFromMeToThirdPartyIsSkipped(t *testing.T) {
	own := jid("15551234567", 21)
	// The account holder messaged a third party from another linked
	// device (e.g. their primary phone). WhatsApp echoes it back to
	// this device with IsFromMe=true, Sender=our own account (possibly
	// as a LID), Chat=the third-party's JID. This must NOT pass through
	// as if the account holder were talking to the agent — otherwise
	// the reply lands in the third-party chat.
	otherLID := lidJID("268285216030760")
	sender := jid("15551234567", 3) // account holder, different device from `own`
	evt := msgEvent(sender, otherLID, true, false, "hey alice")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipOwnOutbound, res.Skip,
		"outbound-to-third-party from another linked device must be skipped")
}

func TestResolveInbound_IsFromMeToThirdPartyViaLIDSenderIsSkipped(t *testing.T) {
	own := jid("15551234567", 21)
	// Same as above but the sender is reported as our own LID (newer
	// WhatsApp behaviour). The guard must still fire.
	lid := lidJID("276540210315282")
	otherPN := jid("447900123456", 0)
	evt := msgEvent(lid, otherPN.ToNonAD(), true, false, "hey bob")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipOwnOutbound, res.Skip,
		"outbound-to-third-party via LID sender must be skipped")
}

func TestResolveInbound_SelfChatViaOwnLIDAsChatAndSender(t *testing.T) {
	// Real-world shape observed in production: WhatsApp reports both
	// Chat and Sender as the account holder's own LID for self-chat.
	// Must be treated as self-chat and rewritten to the account JID
	// so the allowlist matches.
	own := jid("15551234567", 21)
	ownLID := lidJID("276540210315282")
	evt := msgEvent(ownLID, ownLID, true, false, "self via lid")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "15551234567@s.whatsapp.net", res.Msg.From)
}

func TestResolveInbound_LIDSubstitutedToAccountJID(t *testing.T) {
	own := jid("15551234567", 21)
	// Newer WhatsApp reports the account holder's outbound sender as a
	// LID — substitute the account JID so allowlists match.
	lid := lidJID("276540210315282")
	evt := msgEvent(lid, own.ToNonAD(), true, false, "self-chat via lid")
	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "15551234567@s.whatsapp.net", res.Msg.From,
		"LID sender should be rewritten to the account JID")
}

func TestResolveInbound_MultiDeviceSuffixStripped(t *testing.T) {
	own := jid("15551234567", 21)
	// Contact sender with a device suffix — allowlist should still
	// match on the plain phone-number JID.
	sender := jid("15551234567", 3)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	res := ResolveInbound(evt, &own)
	require.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "15551234567@s.whatsapp.net", res.Msg.From)
}

func TestResolveInbound_EmptyBodyIsSkipped(t *testing.T) {
	own := jid("15551234567", 21)
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
	own := jid("15551234567", 21)
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

func TestResolveInbound_LIDSenderWithPNAltUsesPN(t *testing.T) {
	// This is the shape from the operator's incident report: a
	// third party messages the daemon, WhatsApp routes the event
	// via the sender's LID, and the operator's allowlist is in the
	// familiar PN form. SenderAlt exposes the PN — ResolveInbound
	// must prefer it so allowlist matching works.
	own := jid("447000000000", 21)
	lid := lidJID("268285216030760")
	pn := jid("15551234567", 0)
	evt := msgEventWithAlt(lid, pn, lid, false, "hello")

	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "15551234567@s.whatsapp.net", res.Msg.From,
		"third-party LID sender with PN alt must expose the PN to the router")
}

func TestResolveInbound_LIDSenderWithoutPNAltFallsBack(t *testing.T) {
	// When SenderAlt is not populated (older client, LID-only user),
	// we surface the LID as-is — the operator can allowlist the LID
	// explicitly.
	own := jid("447000000000", 21)
	lid := lidJID("268285216030760")
	evt := msgEvent(lid, lid, false, false, "hello")

	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "268285216030760@lid", res.Msg.From,
		"no PN alt → keep the LID so the operator can still allowlist explicitly")
}

func TestResolveInbound_PNSenderUnchangedWhenAltIsLID(t *testing.T) {
	// Symmetric case: some clients report Sender=PN and SenderAlt=LID.
	// PN is preferred either way — Sender is already PN so we keep it.
	own := jid("447000000000", 21)
	pn := jid("15551234567", 0)
	lid := lidJID("268285216030760")
	evt := msgEventWithAlt(pn, lid, pn, false, "hello")

	res := ResolveInbound(evt, &own)
	assert.Equal(t, SkipNone, res.Skip)
	assert.Equal(t, "15551234567@s.whatsapp.net", res.Msg.From)
}
