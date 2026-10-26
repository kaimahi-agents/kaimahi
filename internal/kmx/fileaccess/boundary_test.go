package fileaccess

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProductionSurfaceIsNarrowAndOpaque(t *testing.T) {
	exportedFunctions := map[string]bool{}
	exportedTypes := map[string]bool{}
	banned := map[string]bool{
		"Create": true, "CreateFile": true, "Link": true, "Mkdir": true,
		"MoveFileEx": true, "MkdirAll": true, "OpenFile": true, "Remove": true, "RemoveAll": true,
		"Rename": true, "ReplaceFile": true, "SetEndOfFile": true,
		"SetFileInformationByHandle": true, "SetFileSecurity": true,
		"SetFileTime": true, "SetNamedSecurityInfo": true, "SetSecurityInfo": true,
		"Truncate": true, "WriteAt": true, "WriteFile": true,
	}
	authority := map[string]map[string]bool{
		"Flock":                {"lock_unix.go": true},
		"FlushFileBuffers":     {"syncdir_windows.go": true},
		"Linkat":               {"publish_unix.go": true},
		"LockFileEx":           {"lock_windows.go": true},
		"Mkdirat":              {"create_unix.go": true},
		"NtSetInformationFile": {"create_windows.go": true, "publish_windows.go": true},
		"Sync":                 {"publish.go": true, "syncdir_unix.go": true},
		"Unlinkat":             {"create_unix.go": true, "publish_unix.go": true},
		"Write":                {"publish.go": true},
	}
	windowsAuthority := map[string]map[string]bool{
		"DELETE":             {"create_windows.go": true},
		"FILE_CREATE":        {"create_windows.go": true},
		"FILE_GENERIC_WRITE": {"create_windows.go": true, "syncdir_windows.go": true},
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			switch value := decl.(type) {
			case *ast.FuncDecl:
				if value.Recv == nil && ast.IsExported(value.Name.Name) {
					exportedFunctions[value.Name.Name] = true
				}
			case *ast.GenDecl:
				if value.Tok == token.TYPE {
					for _, spec := range value.Specs {
						name := spec.(*ast.TypeSpec).Name.Name
						if ast.IsExported(name) {
							exportedTypes[name] = true
						}
					}
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.SelectorExpr:
				if allowed, ok := authority[value.Sel.Name]; ok && !allowed[name] {
					t.Errorf("%s calls mutation operation %s outside its exact owner", name, value.Sel.Name)
				} else if banned[value.Sel.Name] && !ok {
					t.Errorf("%s calls unapproved mutation operation %s", name, value.Sel.Name)
				}
			case *ast.Ident:
				if allowed, ok := windowsAuthority[value.Name]; ok && !allowed[name] {
					t.Errorf("%s contains Windows mutation authority %s outside its exact owner", name, value.Name)
				} else if value.Name == "FILE_OPEN_IF" || value.Name == "FILE_SUPERSEDE" || strings.HasPrefix(value.Name, "FILE_OVERWRITE") || value.Name == "GENERIC_WRITE" {
					t.Errorf("%s contains unapproved Windows replacement authority %s", name, value.Name)
				}
			}
			return true
		})
	}
	names := make([]string, 0, len(exportedFunctions))
	for name := range exportedFunctions {
		names = append(names, name)
	}
	slices.Sort(names)
	want := []string{"CheckPrivate", "CheckSafeDirectory", "CreatePrivateDirectoryAt", "CreatePrivateFileAt", "EnsurePrivateDirectory", "EnsurePrivateDirectoryAt", "EnsurePrivateFileAt", "LinkCount", "OpenDirectory", "OpenDirectoryAt", "OpenPrivateDirectory", "OpenPrivateFileAt", "OpenRegularAt", "PhysicalIdentity", "PublishPrivate", "PublishPrivateNamed", "RemovePrivateFileAt", "SyncDirectory", "TryLock"}
	if !slices.Equal(names, want) {
		t.Fatalf("exported functions = %v, want exactly %v", names, want)
	}
	types := make([]string, 0, len(exportedTypes))
	for name := range exportedTypes {
		types = append(types, name)
	}
	slices.Sort(types)
	wantTypes := []string{"AccessError", "Directory", "ErrorClass", "File", "Object", "PrivateFile", "PublicationResult"}
	if !slices.Equal(types, wantTypes) {
		t.Fatalf("exported types = %v, want exactly %v", types, wantTypes)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "doc.go" {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"internal/kmx/app", "internal/kmx/config", "internal/kmx/agentsuite", "internal/kmx/localpreview", "internal/kmx/toolchain"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("%s imports or names operation package %s", name, forbidden)
			}
		}
	}
}

func TestFoundationConsumersAreExplicit(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || strings.Contains(path, string(filepath.Separator)+"internal"+string(filepath.Separator)+"kmx"+string(filepath.Separator)+"fileaccess"+string(filepath.Separator)) {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			if imported.Path.Value == `"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"` {
				if strings.Contains(path, "_test.go") {
					continue
				}
				allowed := []string{
					filepath.Join(root, "internal", "kmx", "app") + string(filepath.Separator),
					filepath.Join(root, "internal", "kmx", "config") + string(filepath.Separator),
					filepath.Join(root, "internal", "kmx", "localpreview") + string(filepath.Separator),
				}
				// The folder settings reader is the sole localtools foundation consumer.
				settingsReader := filepath.Join(root, "internal", "kmx", "localtools", "settings.go")
				settingsWriter := filepath.Join(root, "internal", "kmx", "mcp", "tool_settings.go")
				if path != settingsReader && path != settingsWriter && !slices.ContainsFunc(allowed, func(prefix string) bool { return strings.HasPrefix(path, prefix) }) {
					t.Errorf("production package imports unwired filesystem foundation: %s", path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
