package provider

import "testing"

func TestVersionMatches(t *testing.T) {
	tests := []struct {
		name     string
		declared string
		ref      string
		commit   string
		want     bool
	}{
		{"empty declared always matches", "", "v1.0.0", "abc123", true},
		{"exact tag match", "v1.11.0", "v1.11.0", "0123456789abcdef0123456789abcdef01234567", true},
		{"declared is prefix of commit", "481d9b0", "481d9b0", "481d9b0fedcba9876543210fedcba98765432100", true},
		{"full sha match", "481d9b0fedcba9876543210fedcba98765432100", "v1.0.0-3-g481d9b0", "481d9b0fedcba9876543210fedcba98765432100", true},
		{"ref starts with declared (describe past tag)", "v1.4.6", "v1.4.6-1-gf51d88a", "f51d88a0000000000000000000000000000000000", true},
		{"tag mismatch", "v1.11.0", "v1.10.0", "0123456789abcdef0123456789abcdef01234567", false},
		{"sha mismatch", "deadbeef", "v1.0.0", "0123456789abcdef0123456789abcdef01234567", false},
		{"empty installed info never matches non-empty declared", "v1.0.0", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionMatches(tt.declared, tt.ref, tt.commit); got != tt.want {
				t.Errorf("versionMatches(%q, %q, %q) = %v, want %v", tt.declared, tt.ref, tt.commit, got, tt.want)
			}
		})
	}
}
