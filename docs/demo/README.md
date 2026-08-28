# rousseau-agent demo

A scripted five-minute walkthrough that takes a stranger from empty terminal to a live WhatsApp bridge answering a message.

Two artefacts live here:

- [`onboarding.md`](./onboarding.md) — the human-readable step-by-step. Copy-pasteable commands, expected output blocks, common failure modes.
- [`record.sh`](./record.sh) — an unattended demo script. Pipe it through `asciinema rec` (or a screen recorder) and get a reproducible cast.

## Recording an asciicast

```bash
# One-time setup.
sudo apt install asciinema     # or brew install asciinema

# Record. Cast is saved to onboarding.cast; upload with:
#     asciinema upload onboarding.cast
asciinema rec \
    --title "rousseau-agent: install to first reply in 5 minutes" \
    --cols 100 --rows 30 \
    --command "bash docs/demo/record.sh" \
    docs/demo/onboarding.cast
```

The recording is deliberately **not** committed. Cast files are large binary blobs; publish the cast URL in the README or the release notes.

## Recording an mp4/gif (fallback)

If a viewer's browser cannot play asciicast:

```bash
# From an existing cast:
agg docs/demo/onboarding.cast docs/media/onboarding.gif

# Direct capture:
ffmpeg -f x11grab -s 1920x1080 -i :0.0 -r 30 docs/media/onboarding.mp4
```

`docs/media/` is gitignored by default — publish the file as a GitHub release asset instead, not in the repo.
