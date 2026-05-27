package webrunner

import "testing"

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name     string
		ldflags  string
		buildEnv string
		want     string
	}{
		{"ldflags set properly", "v1.2.3", "", "v1.2.3"},
		{"ldflags dev, buildEnv set", "dev", "0.1.0-dev", "0.1.0-dev"},
		{"ldflags empty, buildEnv set", "", "0.1.0-dev", "0.1.0-dev"},
		{"ldflags dev, buildEnv empty", "dev", "", "dev"},
		{"both empty", "", "", ""},
		{"ldflags has real version, buildEnv also set", "v2.0.0", "0.1.0-dev", "v2.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveVersion(tt.ldflags, tt.buildEnv)
			if got != tt.want {
				t.Errorf("resolveVersion(%q, %q) = %q, want %q", tt.ldflags, tt.buildEnv, got, tt.want)
			}
		})
	}
}
