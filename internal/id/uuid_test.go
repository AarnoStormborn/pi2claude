package id

import "testing"

func TestV5Known(t *testing.T) {
	dns := [16]byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	if got, want := V5(dns, "pi2claude"), "18994cbe-8e97-5e6b-af85-b2762928f488"; got != want {
		t.Fatalf("V5 = %q, want %q", got, want)
	}
}

func TestDeterministic(t *testing.T) {
	a := Deterministic("session", "entry", "0")
	b := Deterministic("session", "entry", "0")
	c := Deterministic("session", "entry", "1")
	if a != b {
		t.Fatalf("not deterministic: %q != %q", a, b)
	}
	if a == c {
		t.Fatalf("different names produced the same uuid")
	}
	if !Valid(a) {
		t.Fatalf("not a valid uuid: %q", a)
	}
}

func TestParse(t *testing.T) {
	if _, ok := Parse("11111111-2222-4333-8444-555555555555"); !ok {
		t.Fatal("valid uuid rejected")
	}
	for _, s := range []string{"", "not-a-uuid", "11111111222243338444555555555555"} {
		if _, ok := Parse(s); ok {
			t.Fatalf("invalid uuid accepted: %q", s)
		}
	}
}
