package main

import "testing"

func TestBuildLabel(t *testing.T) {
	defer func(v, c string) { version, commit = v, c }(version, commit)
	for _, tc := range []struct{ version, commit, want string }{
		{"", "", ""},
		{"v0.4.0-rc.10", "a1b2c3d", "v0.4.0-rc.10 (a1b2c3d)"},
		{"v0.4.0", "", "v0.4.0"},
		{"", "a1b2c3d", "a1b2c3d"},
	} {
		version, commit = tc.version, tc.commit
		if got := buildLabel(); got != tc.want {
			t.Errorf("version %q, commit %q: got %q, want %q", tc.version, tc.commit, got, tc.want)
		}
	}
}
