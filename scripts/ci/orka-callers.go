//go:build ignore

// Audit typed source-reference drift across the Orka path-routing boundary.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const module = "github.com/kaimahi-agents/kaimahi"

type policy struct {
	Prefixes []string `json:"safe_prefixes"`
	Files    []string `json:"safe_files"`
	Markdown []string `json:"safe_direct_markdown"`
	// Routing is independent of the fixed guard partition; accept its schema only.
	Triggers *struct {
		Prefixes     []string `json:"prefixes"`
		Files        []string `json:"files"`
		Globs        []string `json:"globs"`
		ExcludeFiles []string `json:"exclude_files"`
	} `json:"triggers"`
}

type sourceImport struct {
	File string `json:"file"`
	Path string `json:"path"`
}

type manifest struct {
	Imports  []sourceImport `json:"imports"`
	Version  int            `json:"version"`
	Packages []string       `json:"packages"`
}

type listedPackage struct {
	ImportPath, Name, Dir, Export, ForTest string
	GoFiles, CgoFiles                      []string
	ImportMap                              map[string]string
	Module                                 *struct {
		Path, Dir string
		Main      bool
	}
}

// Only review diagnostics constructed from validated metadata may leave the guard.
// Compiler/parser errors can contain source literals and remain private.
type boundaryDrift string

func (e boundaryDrift) Error() string { return string(e) }

func main() {
	audit := len(os.Args) == 2 && os.Args[1] == "--audit"
	var err error
	if len(os.Args) != 1 && !audit {
		err = fmt.Errorf("invalid arguments")
	} else {
		err = run(audit)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Orka caller boundary failed")
		if message, ok := err.(boundaryDrift); ok {
			fmt.Fprintln(os.Stderr, message)
		}
		os.Exit(1)
	}
}

func readJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(value); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func validPath(path string) bool {
	if path == "" || strings.Contains(path, "\\") || strings.HasPrefix(path, "/") {
		return false
	}
	for _, c := range path {
		if c < 32 || c == 127 {
			return false
		}
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (p policy) safe(path string) bool {
	if !validPath(path) {
		return false
	}
	for _, f := range p.Files {
		if f == path {
			return true
		}
	}
	for _, prefix := range p.Prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false // Go source cannot be a direct Markdown fragment.
}

func run(audit bool) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	var p policy
	if err = readJSON("scripts/ci/orka-routing.json", &p); err != nil {
		return err
	}
	if p.Prefixes == nil || p.Files == nil || p.Markdown == nil {
		return fmt.Errorf("missing policy")
	}
	// ./... intentionally excludes nested modules; refuse rather than silently
	// leaving a new safe subtree outside the compiler's source graph.
	if err = filepath.WalkDir(root, func(path string, item fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() && path != root {
			switch item.Name() {
			case ".git", "node_modules", ".vitepress", "vendor":
				return filepath.SkipDir
			}
		}
		if !item.IsDir() && item.Name() == "go.mod" && filepath.Dir(path) != root {
			return fmt.Errorf("nested module")
		}
		return nil
	}); err != nil {
		return err
	}
	actual, err := references(root, p)
	if err != nil {
		return err
	}
	if audit {
		return json.NewEncoder(os.Stdout).Encode(actual)
	}
	var expected manifest
	if err = readJSON("scripts/ci/orka-callers.json", &expected); err != nil {
		return err
	}
	if expected.Version != 3 || expected.Packages == nil || expected.Imports == nil {
		return fmt.Errorf("invalid manifest")
	}
	imports := map[sourceImport]bool{}
	for _, i := range expected.Imports {
		if !validPath(i.File) || !p.safe(i.File) || !startupImport(i.Path) || imports[i] {
			return fmt.Errorf("invalid import approval")
		}
		imports[i] = true
	}
	packages := map[string]bool{}
	for _, name := range expected.Packages {
		if !validPath(name) || !(name == module || strings.HasPrefix(name, module+"/")) || packages[name] {
			return fmt.Errorf("invalid package approval")
		}
		packages[name] = true
	}
	// Approvals persist until explicitly pruned; deleting a reference is harmless.
	for _, i := range actual.Imports {
		if !imports[i] {
			return boundaryDrift(fmt.Sprintf("new startup import: %s -> %s; inspect --audit and review initialization effects before updating scripts/ci/orka-callers.json", i.File, i.Path))
		}
	}
	for _, name := range actual.Packages {
		if !packages[name] {
			return boundaryDrift(fmt.Sprintf("new package dependency: selected code -> %s; keep code within the selected set or inspect --audit and review the package boundary before updating scripts/ci/orka-callers.json", name))
		}
	}
	fmt.Println("Orka caller boundary verified")
	return nil
}

func list(args ...string) ([]listedPackage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", append([]string{"list", "-deps", "-export", "-json", "-tags=", "-race=false"}, args...)...)
	// The qualifier is Linux/amd64 with no race or custom build tags. Do not
	// change the live worker's Go commands or its cache configuration.
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "GOWORK=off")
	data, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var result []listedPackage
	for {
		var p listedPackage
		if err = decoder.Decode(&p); err == io.EOF {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
}

func references(root string, routing policy) (manifest, error) {
	packages, err := list("./...")
	if err != nil {
		return manifest{}, err
	}
	tests, err := list("-test", "./internal/kmx/app")
	if err != nil {
		return manifest{}, err
	}
	packages = append(packages, tests...)
	exports := map[string]string{}
	sources := map[string]map[string]bool{}
	for _, p := range packages {
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
		if p.Module == nil || !p.Module.Main || strings.HasSuffix(p.ImportPath, ".test") {
			continue
		}
		if p.Module.Path != module || len(p.CgoFiles) != 0 {
			return manifest{}, fmt.Errorf("unsupported owned package")
		}
		key := strings.Split(p.ImportPath, " ")[0]
		if sources[key] == nil {
			sources[key] = map[string]bool{}
		}
		for _, name := range p.GoFiles {
			file := name
			if !filepath.IsAbs(file) {
				file = filepath.Join(p.Dir, file)
			}
			stat, err := os.Lstat(file)
			if err != nil || stat.Mode()&os.ModeSymlink != 0 {
				return manifest{}, fmt.Errorf("aliased source")
			}
			path, err := ownedPath(root, name, p.Dir)
			if err != nil || path == "" {
				return manifest{}, fmt.Errorf("unowned source")
			}
			sources[key][path] = true
		}
		if p.Export == "" && len(p.GoFiles) != 0 {
			return manifest{}, fmt.Errorf("missing export")
		}
	}
	fset := token.NewFileSet()
	boundary := map[string]bool{}
	imports := map[sourceImport]bool{}
	checked := map[string]bool{}
	importers := map[string]types.Importer{}
	for _, p := range packages {
		if p.Module == nil || !p.Module.Main || checked[p.ImportPath] || strings.HasSuffix(p.ImportPath, ".test") {
			continue
		}
		checked[p.ImportPath] = true
		if p.ForTest != "" && p.ForTest != module+"/internal/kmx/app" {
			continue
		}
		var files []*ast.File
		for _, name := range p.GoFiles {
			path, err := ownedPath(root, name, p.Dir)
			if err != nil {
				return manifest{}, err
			}
			if path == "" {
				continue
			}
			if strings.HasSuffix(path, "_test.go") && !strings.HasPrefix(path, "internal/kmx/app/") {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(root, path), nil, parser.SkipObjectResolution|parser.ParseComments)
			if err != nil {
				return manifest{}, err
			}
			// Export data preserves adjusted //line names, not physical provenance.
			// Refuse directives in owned source rather than trusting a relabelled file.
			for _, group := range file.Comments {
				for _, comment := range group.List {
					if strings.HasPrefix(comment.Text, "//line ") || strings.HasPrefix(comment.Text, "/*line ") {
						return manifest{}, fmt.Errorf("line directive")
					}
				}
			}
			if routing.safe(path) {
				for _, spec := range file.Imports {
					name, err := strconv.Unquote(spec.Path.Value)
					if err != nil {
						return manifest{}, err
					}
					if startupImport(name) {
						imports[sourceImport{path, name}] = true
					}
				}
			}
			files = append(files, file)
		}
		if len(files) == 0 {
			continue
		} // Packages containing only unrelated tests.
		lookup := func(path string) (io.ReadCloser, error) {
			if mapped := p.ImportMap[path]; mapped != "" {
				path = mapped
			}
			if exports[path] == "" {
				return nil, fmt.Errorf("missing import export")
			}
			return os.Open(exports[path])
		}
		importMap, err := json.Marshal(p.ImportMap)
		if err != nil {
			return manifest{}, err
		}
		key := string(importMap)
		if importers[key] == nil {
			importers[key] = importer.ForCompiler(fset, "gc", lookup)
		}
		info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
		cfg := types.Config{Importer: importers[key], Sizes: types.SizesFor("gc", "amd64")}
		_, err = cfg.Check(strings.Split(p.ImportPath, " ")[0], fset, files, info)
		if err != nil {
			return manifest{}, err
		}
		for ident, obj := range info.Uses {
			if obj.Pkg() == nil || !(obj.Pkg().Path() == module || strings.HasPrefix(obj.Pkg().Path(), module+"/")) {
				continue
			}
			caller, err := ownedPath(root, fset.PositionFor(ident.Pos(), false).Filename, root)
			if err != nil || caller == "" {
				return manifest{}, fmt.Errorf("unmapped caller")
			}
			if !routing.safe(caller) {
				continue
			}
			callee, err := ownedPath(root, fset.PositionFor(obj.Pos(), false).Filename, root)
			if err != nil || callee == "" || !sources[obj.Pkg().Path()][callee] {
				return manifest{}, fmt.Errorf("unmapped definition")
			}
			if !routing.safe(callee) {
				boundary[obj.Pkg().Path()] = true
			}
		}
	}
	result := manifest{Version: 3, Imports: []sourceImport{}, Packages: []string{}}
	for i := range imports {
		result.Imports = append(result.Imports, i)
	}
	for name := range boundary {
		result.Packages = append(result.Packages, name)
	}
	sortManifest(&result)
	return result, nil
}

func ownedPath(root, name, dir string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("missing position")
	}
	if strings.HasPrefix(name, module+"/") {
		name = filepath.Join(root, strings.TrimPrefix(name, module+"/"))
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(dir, name)
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", err
	}
	path, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	path = filepath.ToSlash(path)
	if path == ".." || strings.HasPrefix(path, "../") {
		return "", nil
	}
	if !validPath(path) {
		return "", fmt.Errorf("invalid source path")
	}
	return path, nil
}

// These packages register handlers on the shared default HTTP mux at import
// time, even when their named objects are used only inside an uncalled function.
func startupImport(path string) bool {
	return path == "net/http/pprof" || path == "expvar"
}

func sortManifest(m *manifest) {
	sort.Slice(m.Imports, func(i, j int) bool {
		if m.Imports[i].File != m.Imports[j].File {
			return m.Imports[i].File < m.Imports[j].File
		}
		return m.Imports[i].Path < m.Imports[j].Path
	})
	sort.Strings(m.Packages)
}
