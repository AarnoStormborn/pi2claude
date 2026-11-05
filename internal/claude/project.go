// Package claude contains Claude Code's on-disk conventions.
package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// MaxSanitizedLength is Claude Code's project directory name limit.
const MaxSanitizedLength = 200

// SanitizePath replaces every non-ASCII-alphanumeric character with '-'.
// This matches Claude Code's `e.replace(/[^a-zA-Z0-9]/g, "-")`.
func SanitizePath(p string) string {
	var b strings.Builder
	// JavaScript's regex operates on UTF-16 code units. Iterating the same
	// units also keeps the 200-character limit identical for non-ASCII paths.
	for _, unit := range utf16.Encode([]rune(p)) {
		if (unit >= 'a' && unit <= 'z') || (unit >= 'A' && unit <= 'Z') || (unit >= '0' && unit <= '9') {
			b.WriteByte(byte(unit))
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// JavaHash reproduces JavaScript's `(e << 5) - e + charCodeAt(i) | 0` over
// UTF-16 code units. Claude Code uses it for long project directory names.
func JavaHash(s string) int32 {
	var h int32
	for _, unit := range utf16.Encode([]rune(s)) {
		h = (h << 5) - h + int32(unit)
	}
	return h
}

// hashSuffix is `Math.abs(JavaHash(s)).toString(36)`.
func hashSuffix(s string) string {
	n := int64(JavaHash(s))
	if n < 0 {
		n = -n
	}
	return strconv.FormatInt(n, 36)
}

// ProjectDirName returns the directory name Claude Code uses for cwd.
// The caller should pass a canonical (symlink-resolved) absolute path when
// possible, because Claude Code compares and stores the real path.
func ProjectDirName(cwd string) string {
	n := SanitizePath(cwd)
	if len(n) <= MaxSanitizedLength {
		return n
	}
	return n[:MaxSanitizedLength] + "-" + hashSuffix(cwd)
}

// CanonicalPath returns an absolute, cleaned, symlink-resolved path when the
// path exists. It never fails: an unresolvable path is returned cleaned.
func CanonicalPath(p string) string {
	if p == "" {
		if wd, err := os.Getwd(); err == nil {
			p = wd
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return p
}

// ProjectDir returns ~/.claude/projects/<encoded-cwd>.
func ProjectDir(claudeDir, cwd string) string {
	return filepath.Join(claudeDir, "projects", ProjectDirName(cwd))
}

// TranscriptPath returns the default transcript path for a session.
func TranscriptPath(claudeDir, cwd, sessionID string) string {
	return filepath.Join(ProjectDir(claudeDir, cwd), sessionID+".jsonl")
}
