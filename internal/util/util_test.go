package util

import "testing"

// TestIsUUID covers the version-agnostic UUID guard used by the codex adapter. The key case
// is a UUIDv7 (codex's session id shape): IsUUIDv4 rejects it, IsUUID must accept it. Both
// must reject vibecast's short stream ids — the reason the guard exists.
func TestIsUUID(t *testing.T) {
	cases := []struct {
		name    string
		s       string
		wantAny bool // IsUUID
		wantV4  bool // IsUUIDv4
	}{
		{"uuidv4", "c5e6a2bc-5aba-43fc-9d86-b8d66b481d39", true, true},
		{"uuidv7 (codex)", "019f4cf6-5e8d-7abc-8def-0123456789ab", true, false},
		{"uuidv1", "d94e3f00-5aba-13fc-8d86-b8d66b481d39", true, false},
		{"short vibecast id", "short123", false, false},
		{"empty", "", false, false},
		{"version 0 rejected", "019f4cf6-5e8d-0abc-8def-0123456789ab", false, false},
		{"bad variant rejected", "019f4cf6-5e8d-7abc-cdef-0123456789ab", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsUUID(c.s); got != c.wantAny {
				t.Errorf("IsUUID(%q) = %v, want %v", c.s, got, c.wantAny)
			}
			if got := IsUUIDv4(c.s); got != c.wantV4 {
				t.Errorf("IsUUIDv4(%q) = %v, want %v", c.s, got, c.wantV4)
			}
		})
	}
}
