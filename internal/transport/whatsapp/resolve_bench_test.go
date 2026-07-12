package whatsapp

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// BenchmarkResolveInbound_ContactMessage is the hot path on every
// inbound WhatsApp message. Target: <5µs on modern hardware.
func BenchmarkResolveInbound_ContactMessage(b *testing.B) {
	own := types.JID{User: "447906009073", Server: "s.whatsapp.net", Device: 21}
	sender := types.JID{User: "15551234567", Server: "s.whatsapp.net"}
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Sender: sender, Chat: sender.ToNonAD()},
			Timestamp:     time.Unix(1_700_000_000, 0),
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ResolveInbound(evt, &own)
	}
}

// BenchmarkResolveInbound_LIDSelfChat exercises the LID → account JID
// substitution — the branch that fires on every "message yourself"
// message and any newer-client contact.
func BenchmarkResolveInbound_LIDSelfChat(b *testing.B) {
	own := types.JID{User: "447906009073", Server: "s.whatsapp.net", Device: 21}
	lid := types.JID{User: "276540210315282", Server: "lid"}
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: lid, Chat: own.ToNonAD(), IsFromMe: true,
			},
			Timestamp: time.Unix(1_700_000_000, 0),
		},
		Message: &waProto.Message{Conversation: proto.String("self-chat via lid")},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ResolveInbound(evt, &own)
	}
}

// BenchmarkPrependHeader — cheap but on every reply.
func BenchmarkPrependHeader(b *testing.B) {
	body := "This is the assistant's answer, a couple of sentences long. It contains no meaningful content for this benchmark."
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = PrependHeader(body, "")
	}
}
