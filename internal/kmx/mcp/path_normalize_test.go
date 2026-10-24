package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeAgentPath(t *testing.T) {
	mounts := t.TempDir()
	if err := os.Mkdir(filepath.Join(mounts, "c"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mounts, "n"), []byte("not a drive directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, input, goos, want, code string
	}{
		{"backslash and uppercase", `C:\Users\Team café\agent.agent.md`, "linux", filepath.Join(mounts, "c", "Users", "Team café", "agent.agent.md"), ""},
		{"forward and lowercase", "c:/Users/项目/agent.agent.md", "linux", filepath.Join(mounts, "c", "Users", "项目", "agent.agent.md"), ""},
		{"mixed separators", `c:/folder\nested/agent.agent.md`, "linux", filepath.Join(mounts, "c", "folder", "nested", "agent.agent.md"), ""},
		{"charter", `C:\repo\.squad\agents\greeter\charter.md`, "linux", filepath.Join(mounts, "c", "repo", ".squad", "agents", "greeter", "charter.md"), ""},
		{"volume root parent", `C:\..\agent.agent.md`, "linux", filepath.Join(mounts, "c", "agent.agent.md"), ""},
		{"clean inside volume", `C:\repo\unused\..\agent.agent.md`, "linux", filepath.Join(mounts, "c", "repo", "agent.agent.md"), ""},
		{"missing target parents", `C:\new\nested\agent.agent.md`, "linux", filepath.Join(mounts, "c", "new", "nested", "agent.agent.md"), ""},
		{"mapped mounted drive", filepath.Join(mounts, "c", "new", "agent.agent.md"), "linux", filepath.Join(mounts, "c", "new", "agent.agent.md"), ""},
		{"mapped missing drive", filepath.Join(mounts, "z", "repo", "agent.agent.md"), "linux", "", "unsafe_path"},
		{"mapped missing drive root", filepath.Join(mounts, "z"), "linux", "", "unsafe_path"},
		{"mapped non-directory drive", filepath.Join(mounts, "n", "agent.agent.md"), "linux", "", "unsafe_path"},
		{"ordinary mount directory", filepath.Join(mounts, "ordinary", "agent.agent.md"), "linux", filepath.Join(mounts, "ordinary", "agent.agent.md"), ""},
		{"outside mount base", "/ordinary/z/agent.agent.md", "linux", "/ordinary/z/agent.agent.md", ""},
		{"other Unix mapped path unchanged", "/mnt/z/agent.agent.md", "darwin", "/mnt/z/agent.agent.md", ""},
		{"missing drive", `Z:\repo\agent.agent.md`, "linux", "", "unsafe_path"},
		{"drive is a file", `N:\repo\agent.agent.md`, "linux", "", "unsafe_path"},
		{"drive relative", `C:agent.agent.md`, "linux", "", "unsafe_path"},
		{"bare drive", "C:", "linux", "", "unsafe_path"},
		{"UNC", `\\wsl.localhost\Ubuntu\mnt\c\agent.agent.md`, "linux", "", "unsafe_path"},
		{"device", `\\?\C:\agent.agent.md`, "linux", "", "unsafe_path"},
		{"device namespace", `\\.\C:\agent.agent.md`, "linux", "", "unsafe_path"},
		{"other Unix", `C:\agent.agent.md`, "darwin", "", "unsafe_path"},
		{"ordinary relative", "./folder/agent.agent.md", "linux", "./folder/agent.agent.md", ""},
		{"ordinary absolute", "/ordinary/folder/agent.agent.md", "linux", "/ordinary/folder/agent.agent.md", ""},
		{"POSIX double slash", "//ordinary/folder/agent.agent.md", "linux", "//ordinary/folder/agent.agent.md", ""},
		{"relative backslash unchanged", `folder\agent.agent.md`, "linux", `folder\agent.agent.md`, ""},
		{"not an ASCII drive", `1:\agent.agent.md`, "linux", `1:\agent.agent.md`, ""},
		{"native Windows", `C:\Users\Team\agent.agent.md`, "windows", `C:\Users\Team\agent.agent.md`, ""},
		{"native UNC", `\\server\share\agent.agent.md`, "windows", `\\server\share\agent.agent.md`, ""},
		{"native drive relative", `C:agent.agent.md`, "windows", `C:agent.agent.md`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeAgentPath(tt.input, tt.goos, mounts)
			if tt.code != "" {
				requireCode(t, err, tt.code)
				if got != "" {
					t.Fatal("refused mapping retained a path")
				}
			} else if err != nil || got != tt.want {
				t.Fatalf("path = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	for _, absent := range []string{"z", filepath.Join("c", "new")} {
		if _, err := os.Stat(filepath.Join(mounts, absent)); !os.IsNotExist(err) {
			t.Fatalf("normalization created a missing drive or target directory: %v", err)
		}
	}
}
