package id

import (
	"strings"
	"testing"
)

func TestNewUUIDv7(t *testing.T) {
	first, err := NewUUIDv7()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewUUIDv7()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{first, second} {
		if len(value) != 36 || strings.Count(value, "-") != 4 {
			t.Fatalf("invalid UUID format %q", value)
		}
		if value[14] != '7' {
			t.Errorf("UUID version = %q, want 7", value[14])
		}
		if !strings.ContainsAny(value[19:20], "89ab") {
			t.Errorf("UUID variant = %q", value[19])
		}
	}
}
