package claude

import (
	"strings"
	"testing"
)

func TestSanitizePath(t *testing.T) {
	cases := map[string]string{
		"/Users/me/code/app":    "-Users-me-code-app",
		"/tmp/a.b_c d/e.f":      "-tmp-a-b-c-d-e-f",
		"/Users/me/.config/foo": "-Users-me--config-foo",
		`C:\Users\me\code`:      "C--Users-me-code",
	}
	for in, want := range cases {
		if got := SanitizePath(in); got != want {
			t.Errorf("SanitizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJavaHashLongPathSuffix(t *testing.T) {
	p := "/private/tmp/cc-probe/long"
	for i := 1; i <= 30; i++ {
		p += "/segment" + itoa(i)
	}
	got := ProjectDirName(p)
	if !strings.HasSuffix(got, "-20u9hl") {
		t.Fatalf("ProjectDirName(%q) suffix = %q, want -20u9hl", p, got[len(got)-20:])
	}
	if len(got) != MaxSanitizedLength+1+6 {
		t.Fatalf("length = %d, want %d", len(got), MaxSanitizedLength+1+6)
	}
	if !strings.HasPrefix(got, SanitizePath(p)[:MaxSanitizedLength]) {
		t.Fatalf("prefix does not match sanitized path")
	}
}

func TestProjectDirNameShortUnchanged(t *testing.T) {
	p := "/Users/me/code/app"
	if got, want := ProjectDirName(p), SanitizePath(p); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
