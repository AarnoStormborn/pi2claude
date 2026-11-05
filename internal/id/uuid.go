// Package id provides the small amount of UUID handling pi2claude needs.
package id

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
)

// Namespace is the fixed UUIDv5 namespace used for every generated id.
// It is the UUIDv5 of the DNS namespace and "pi2claude".
var Namespace = [16]byte{
	0x18, 0x99, 0x4c, 0xbe, 0x8e, 0x97, 0x5e, 0x6b,
	0xaf, 0x85, 0xb2, 0x76, 0x29, 0x28, 0xf4, 0x88,
}

// V5 returns a deterministic UUIDv5 for name in namespace.
func V5(namespace [16]byte, name string) string {
	h := sha1.New()
	_, _ = h.Write(namespace[:])
	_, _ = h.Write([]byte(name))
	sum := h.Sum(nil)
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	return Format(u)
}

// Format renders a UUID in canonical 8-4-4-4-12 form.
func Format(u [16]byte) string {
	var b strings.Builder
	b.Grow(36)
	for i, v := range u {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			b.WriteByte('-')
		}
		const hexdigits = "0123456789abcdef"
		b.WriteByte(hexdigits[v>>4])
		b.WriteByte(hexdigits[v&0x0f])
	}
	return b.String()
}

// Parse parses a UUID-shaped string. It accepts uppercase input but only the
// canonical hyphenated form that Claude Code validates.
func Parse(s string) ([16]byte, bool) {
	var u [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, false
	}
	raw := strings.ReplaceAll(s, "-", "")
	if len(raw) != 32 {
		return u, false
	}
	b, err := hex.DecodeString(raw)
	if err != nil {
		return u, false
	}
	copy(u[:], b)
	return u, true
}

// Valid reports whether s is a canonical hyphenated UUID.
func Valid(s string) bool {
	_, ok := Parse(s)
	return ok
}

// Deterministic returns a UUIDv5 derived from parts joined by ":".
func Deterministic(parts ...string) string {
	return V5(Namespace, strings.Join(parts, ":"))
}

// HashHex returns the first n hex characters of SHA-1(parts joined by ":").
// It is used for opaque API ids such as msg_/req_ prefixes.
func HashHex(n int, parts ...string) string {
	h := sha1.New()
	_, _ = h.Write([]byte(strings.Join(parts, ":")))
	s := hex.EncodeToString(h.Sum(nil))
	if n > len(s) {
		n = len(s)
	}
	return s[:n]
}

// ValidateShape returns an error if s is not a canonical UUID.
func ValidateShape(s string) error {
	if !Valid(s) {
		return fmt.Errorf("not a canonical UUID: %q", s)
	}
	return nil
}
