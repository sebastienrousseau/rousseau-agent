package audio

// ExtensionForMime exposes the unexported mime→file-extension mapper
// to the external test package. Its final "unknown mime" branch is
// unreachable through Transcribe (both backends gate on
// KnownVoiceNoteMimeType first), so it can only be pinned directly.
var ExtensionForMime = extensionForMime
