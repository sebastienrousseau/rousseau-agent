# Package `audio`

Voice-note → text pipeline for inbound-audio-carrying transports.

## Backends

| Backend | Kind() | Trust profile | Requires |
|---|---|---|---|
| [`WhisperCPP`](./whisper_cpp.go) | `whisper-cpp` | audio stays on your host | `whisper-cli` (or `whisper` / `main`) on `$PATH` + a ggml-*.bin model file |
| [`OpenAIAPI`](./openai_api.go) | `openai-api` | audio sent to OpenAI | `OPENAI_API_KEY`, outbound HTTPS to `api.openai.com` (or `BaseURL` for OpenAI-compatible providers) |
| [`Noop`](./noop.go) | `noop` | deterministic stub | nothing — used in tests |

## Config

```yaml
media:
  audio:
    backend: whisper-cpp           # whisper-cpp | openai-api | ""(disabled)
    # whisper-cpp knobs
    model_file: /models/ggml-base.en.bin
    binary: whisper-cli
    # openai-api knobs
    api_key: ${OPENAI_API_KEY}
    base_url: https://api.openai.com/v1   # override for compatible providers
    model: whisper-1
    # both
    language: en                    # ISO 639-1 hint (empty = auto-detect)
    timeout_seconds: 60             # 0 = backend default (60 whisper-cpp / 90 openai-api)
    max_bytes: 26214400             # 0 = backend default (16 MiB whisper / 25 MiB openai)
```

## Transport wiring status

| Transport | Voice inbound wired | How |
|---|---|---|
| **WhatsApp** | ✅ | Preexisting `Config.Transcriber` slot; `internal/cli/whatsapp.go` prefers `media.audio` over legacy `whatsapp.voice.enabled` |
| Telegram | ⏳ | Add `Transcriber` to `internal/transport/telegram.Config`; in Dispatch, detect `Voice` / `Audio` update, download via bot API, call `Transcriber.Transcribe` |
| iMessage | ⏳ | Same pattern: extend Config, add audio-detect branch in Dispatch (AppleScript source), download attachment, transcribe |
| Signal | ⏳ | Same pattern for `signal-cli` JSON-RPC — audio attachments are on the message envelope |
| Discord | ⏳ | Voice messages are `attachments[].content_type=audio/ogg` on the message payload |
| Matrix | ⏳ | `m.audio` events carry an `mxc://` URL to fetch |

For each unwired transport the porting recipe is:

```go
// 1. In internal/transport/<X>/types.go, add to Config:
type Config struct {
    // …existing fields…
    Transcriber Transcriber   // matches the shape in whatsapp/types.go
}

// 2. In dispatch: detect the transport's audio-event shape, download
//    the bytes, call in.Transcriber.Transcribe(ctx, bytes, mimetype),
//    then feed the text into the router as if the user had typed it.

// 3. In internal/cli/<X>.go, populate the Config field from
//    buildTranscriberString(opts.Config.Media.Audio) — the same
//    single-line call whatsapp.go uses today.
```

Every step is protocol-specific but the audio pipeline itself is shared. The [W2.1 CHANGELOG entry](../../../CHANGELOG.md) tracks completion per transport.

## Metrics

The backend emits `rousseau_audio_transcribed_total{transport,backend,status}` and `rousseau_audio_transcribe_seconds{transport,backend}` (planned — instrumentation lands with the per-transport wiring above).

## Design invariants

- **Errors are non-fatal.** A failed transcription returns a Go error to the transport, which logs it and skips the message rather than replying to the user with a synthetic error. Users see "no reply"; operators see the WARN.
- **Backends are stateless.** Every call is self-contained; no session, no cache. This lets the same backend serve every transport concurrently without a lock.
- **Mime-type filter first.** [`KnownVoiceNoteMimeType`](./audio.go) short-circuits before the backend sees a video/mp4 or a text/plain — cheap rejection, no spend.
