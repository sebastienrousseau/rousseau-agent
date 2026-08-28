package media

import "testing"

// BenchmarkPolicy_Accept establishes the per-call cost of Accept on a
// real PNG so operators can size the transport-side sniff overhead.
// Every image-capable transport calls this once per incoming attachment
// before pushing bytes into an agent.Message.
func BenchmarkPolicy_Accept(b *testing.B) {
	data := benchPNG()
	p := Policy{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Accept(data, 0); err != nil {
			b.Fatalf("accept: %v", err)
		}
	}
}

// BenchmarkPolicy_Reject measures the deny path (unknown MIME) since
// hostile senders can spam it and it must not eat CPU.
func BenchmarkPolicy_Reject(b *testing.B) {
	data := []byte("this is not an image, it is a very long plain-text payload " +
		"designed to force the sniffer to run to its length limit before " +
		"deciding it isn't PNG / JPEG / WebP / GIF and returning the deny")
	p := Policy{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Accept(data, 0); err == nil {
			b.Fatal("expected reject")
		}
	}
}

// benchPNG returns a minimal 1×1 PNG suitable for benchmark inputs.
func benchPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x5B, 0xA0, 0xBF,
		0x51, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E,
		0x44, 0xAE, 0x42, 0x60, 0x82,
	}
}
