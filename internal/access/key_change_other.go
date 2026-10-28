//go:build !darwin && !linux

package access

import (
	"os"
	"time"
)

// FileInfo exposes no portable change time here. Validate the fingerprint
// instead, so a same-size rewrite with restored mtime cannot return stale rows.
func keyChangeTime(os.FileInfo) time.Time { return time.Time{} }
