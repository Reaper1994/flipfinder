package main

import (
	"strings"
	"testing"
)

func TestKeyKind(t *testing.T) {
	for _, c := range []struct{ key, sep, want string }{
		{"Name-App-PRD-1234abcd-5678", "-", "production"},
		{"Name-App-SBX-1234abcd-5678", "-", "SANDBOX"},
		{"PRD-1234abcd-5678", "", "production"},
		{"SBX-1234abcd-5678", "", "SANDBOX"},
		{"dc3d93d8-cbc3-4678", "", "UNRECOGNISED"}, // e.g. a Dev ID pasted as the secret
	} {
		if got := keyKind(c.key, c.sep); !strings.HasPrefix(got, c.want) {
			t.Errorf("keyKind(%q) = %q, want %s", c.key, got, c.want)
		}
	}
}
