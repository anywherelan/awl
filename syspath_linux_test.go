//go:build linux && !android

package awl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendToolDirs(t *testing.T) {
	root := t.TempDir()
	dir := func(name string) string {
		p := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(p, 0o755))
		return p
	}
	tool := func(dir, name string, mode os.FileMode) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), mode))
	}

	usrBin := dir("usr/bin")
	tool(usrBin, "systemctl", 0o755)
	usrSbin := dir("usr/sbin")
	tool(usrSbin, "iptables", 0o755)

	nixos := dir("nixos/bin")
	tool(nixos, "iptables", 0o755)
	tool(nixos, "ip6tables", 0o755)
	tool(nixos, "resolvconf", 0o755)

	noExec := dir("noexec")
	tool(noExec, "iptables", 0o644)
	subdir := dir("subdir")
	dir("subdir/iptables")

	empty := dir("empty")
	missing := filepath.Join(root, "missing")

	join := func(dirs ...string) string { return strings.Join(dirs, string(filepath.ListSeparator)) }

	tests := []struct {
		name         string
		path         string
		tools        []string
		dirs         []string
		wantPath     string
		wantAppended []appendedDir
	}{
		{
			name:     "tool in PATH, nothing appended",
			path:     join(usrBin, usrSbin),
			tools:    []string{"iptables", "systemctl"},
			dirs:     []string{nixos},
			wantPath: join(usrBin, usrSbin),
		},
		{
			name:         "tool missing from PATH, its dir appended",
			path:         usrBin,
			tools:        []string{"iptables"},
			dirs:         []string{usrSbin},
			wantPath:     join(usrBin, usrSbin),
			wantAppended: []appendedDir{{dir: usrSbin, tools: []string{"iptables"}}},
		},
		{
			name:         "first matching dir wins",
			path:         usrBin,
			tools:        []string{"iptables"},
			dirs:         []string{missing, empty, nixos, usrSbin},
			wantPath:     join(usrBin, nixos),
			wantAppended: []appendedDir{{dir: nixos, tools: []string{"iptables"}}},
		},
		{
			name:         "several tools in one dir, dir appended once",
			path:         usrBin,
			tools:        []string{"iptables", "ip6tables", "resolvconf", "systemctl"},
			dirs:         []string{nixos},
			wantPath:     join(usrBin, nixos),
			wantAppended: []appendedDir{{dir: nixos, tools: []string{"iptables", "ip6tables", "resolvconf"}}},
		},
		{
			name:         "tools from different dirs",
			path:         "",
			tools:        []string{"systemctl", "iptables"},
			dirs:         []string{usrBin, usrSbin},
			wantPath:     join(usrBin, usrSbin),
			wantAppended: []appendedDir{{dir: usrBin, tools: []string{"systemctl"}}, {dir: usrSbin, tools: []string{"iptables"}}},
		},
		{
			name:         "non-executable file and directory are skipped",
			path:         usrBin,
			tools:        []string{"iptables"},
			dirs:         []string{noExec, subdir, usrSbin},
			wantPath:     join(usrBin, usrSbin),
			wantAppended: []appendedDir{{dir: usrSbin, tools: []string{"iptables"}}},
		},
		{
			name:         "non-executable file in PATH does not count",
			path:         noExec,
			tools:        []string{"iptables"},
			dirs:         []string{usrSbin},
			wantPath:     join(noExec, usrSbin),
			wantAppended: []appendedDir{{dir: usrSbin, tools: []string{"iptables"}}},
		},
		{
			name:     "tool found nowhere, PATH unchanged",
			path:     usrBin,
			tools:    []string{"resolvconf"},
			dirs:     []string{missing, empty, usrSbin},
			wantPath: usrBin,
		},
		{
			name:         "relative dirs are ignored",
			path:         "",
			tools:        []string{"iptables"},
			dirs:         []string{"usr/sbin", usrSbin},
			wantPath:     usrSbin,
			wantAppended: []appendedDir{{dir: usrSbin, tools: []string{"iptables"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, gotAppended := appendToolDirs(tt.path, tt.tools, tt.dirs)
			require.Equal(t, tt.wantPath, gotPath)
			require.Equal(t, tt.wantAppended, gotAppended)
		})
	}
}
