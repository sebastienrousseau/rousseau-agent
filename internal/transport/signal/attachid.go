package signal

import (
	"path/filepath"
	"strings"
)

// validAttachmentID reports whether id is a bare file name that stays
// inside AttachmentsDir when joined onto it. signal-cli ids are
// opaque names; anything with a separator, or "." / "..", is a path
// and is refused rather than read.
func validAttachmentID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	return filepath.Base(id) == id && !strings.ContainsAny(id, `/\`)
}
