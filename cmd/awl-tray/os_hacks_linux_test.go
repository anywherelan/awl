//go:build linux

package main

import "testing"

func TestAppendPathDirs(t *testing.T) {
	const pkexecPath = "/usr/sbin:/usr/bin:/sbin:/bin:/root/bin"

	tests := []struct {
		name  string
		base  string
		extra string
		want  string
	}{
		{
			name:  "nixos user path goes after pkexec path",
			base:  pkexecPath,
			extra: "/home/u/bin:/run/wrappers/bin:/home/u/.nix-profile/bin:/run/current-system/sw/bin",
			want:  pkexecPath + ":/home/u/bin:/run/wrappers/bin:/home/u/.nix-profile/bin:/run/current-system/sw/bin",
		},
		{
			name:  "debian user path without sbin keeps sbin from pkexec",
			base:  pkexecPath,
			extra: "/usr/local/bin:/usr/bin:/bin:/usr/local/games:/usr/games",
			want:  pkexecPath + ":/usr/local/bin:/usr/local/games:/usr/games",
		},
		{
			name:  "duplicates inside extra are dropped",
			base:  "/usr/bin",
			extra: "/home/u/bin:/home/u/bin:/usr/bin",
			want:  "/usr/bin:/home/u/bin",
		},
		{
			name:  "relative and empty entries are dropped",
			base:  "/usr/bin",
			extra: "::.:bin:./bin:/home/u/bin",
			want:  "/usr/bin:/home/u/bin",
		},
		{
			name:  "empty extra keeps base",
			base:  pkexecPath,
			extra: "",
			want:  pkexecPath,
		},
		{
			name:  "empty base",
			base:  "",
			extra: "/usr/bin:/bin",
			want:  "/usr/bin:/bin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendPathDirs(tt.base, tt.extra)
			if got != tt.want {
				t.Errorf("appendPathDirs(%q, %q)\n got: %q\nwant: %q", tt.base, tt.extra, got, tt.want)
			}
		})
	}
}
