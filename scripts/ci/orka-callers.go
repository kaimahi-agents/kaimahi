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
	"reflect"
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
}

type edge struct {
	Caller       string `json:"caller"`
	CallerObject string `json:"caller_object"`
	Callee       string `json:"callee"`
	Object       string `json:"object"`
}

type sourceImport struct {
	File string `json:"file"`
	Path string `json:"path"`
}

type manifest struct {
	Imports []sourceImport `json:"imports"`
	Version int            `json:"version"`
	Edges   []edge         `json:"edges"`
	FromRun []string       `json:"safe_from_run"`
	ToRun   []string       `json:"safe_to_run"`
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

func main() {
	audit := len(os.Args) == 2 && os.Args[1] == "--audit"
	if (len(os.Args) != 1 && !audit) || run(audit) != nil {
		// Compiler failures can contain source literals; never forward them.
		fmt.Fprintln(os.Stderr, "Orka caller boundary failed")
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
	if expected.Version != 1 || expected.Edges == nil || expected.FromRun == nil || expected.ToRun == nil || expected.Imports == nil {
		return fmt.Errorf("invalid manifest")
	}
	for _, i := range expected.Imports {
		if !validPath(i.File) || !validPath(i.Path) {
			return fmt.Errorf("invalid import")
		}
	}
	seen := map[edge]bool{}
	for _, e := range expected.Edges {
		if !validPath(e.Caller) || !validPath(e.Callee) || e.CallerObject == "" || e.Object == "" || seen[e] {
			return fmt.Errorf("invalid edge")
		}
		seen[e] = true
	}
	sortManifest(&expected)
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("reference drift")
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
	boundary := map[edge]bool{}
	imports := map[sourceImport]bool{}
	graph := map[string]map[string]bool{}
	checked := map[string]bool{}
	importers := map[string]types.Importer{}
	fields := map[*types.Var]string{}
	fieldPackages := map[*types.Package]bool{}
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
					imports[sourceImport{path, name}] = true
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
		info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
		cfg := types.Config{Importer: importers[key], Sizes: types.SizesFor("gc", "amd64")}
		_, err = cfg.Check(strings.Split(p.ImportPath, " ")[0], fset, files, info)
		if err != nil {
			return manifest{}, err
		}
		collectFields := func(obj types.Object) {
			if obj == nil {
				return
			}
			if named, ok := types.Unalias(obj.Type()).(*types.Named); ok {
				if body, ok := named.Underlying().(*types.Struct); ok {
					for i := 0; i < body.NumFields(); i++ {
						fields[body.Field(i)] = named.Obj().Name()
					}
				}
			}
		}
		for _, obj := range info.Defs {
			if _, ok := obj.(*types.TypeName); ok {
				collectFields(obj)
			}
		}
		for _, obj := range info.Uses {
			if obj.Pkg() != nil && strings.HasPrefix(obj.Pkg().Path(), module+"/") && !fieldPackages[obj.Pkg()] {
				fieldPackages[obj.Pkg()] = true
				for _, name := range obj.Pkg().Scope().Names() {
					if value, ok := obj.Pkg().Scope().Lookup(name).(*types.TypeName); ok {
						collectFields(value)
					}
				}
			}
		}
		sites := callerSites(files, info, fields)
		for ident, obj := range info.Uses {
			if obj.Pkg() == nil || !(obj.Pkg().Path() == module || strings.HasPrefix(obj.Pkg().Path(), module+"/")) {
				continue
			}
			caller, err := ownedPath(root, fset.PositionFor(ident.Pos(), false).Filename, root)
			if err != nil || caller == "" {
				return manifest{}, fmt.Errorf("unmapped caller")
			}
			callee, err := ownedPath(root, fset.PositionFor(obj.Pos(), false).Filename, root)
			if err != nil || callee == "" || !sources[obj.Pkg().Path()][callee] {
				return manifest{}, fmt.Errorf("unmapped definition")
			}
			if caller == callee {
				continue
			}
			if graph[caller] == nil {
				graph[caller] = map[string]bool{}
			}
			graph[caller][callee] = true
			if routing.safe(caller) != routing.safe(callee) {
				name := objectKey(obj, callee, fields)
				if sites[ident] == "" {
					return manifest{}, fmt.Errorf("unmapped calling declaration")
				}
				boundary[edge{caller, sites[ident], callee, name}] = true
			}
		}
	}
	result := manifest{Version: 1, Imports: []sourceImport{}, Edges: []edge{}, FromRun: []string{}, ToRun: []string{}}
	for i := range imports {
		result.Imports = append(result.Imports, i)
	}
	for e := range boundary {
		result.Edges = append(result.Edges, e)
	}
	reverse := map[string]map[string]bool{}
	for a, destinations := range graph {
		for b := range destinations {
			if reverse[b] == nil {
				reverse[b] = map[string]bool{}
			}
			reverse[b][a] = true
		}
	}
	result.FromRun = reachableSafe(graph, routing)
	result.ToRun = reachableSafe(reverse, routing)
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

func objectKey(obj types.Object, file string, fields map[*types.Var]string) string {
	base := obj.Pkg().Path() + "."
	if f, ok := obj.(*types.Func); ok {
		if receiver := f.Type().(*types.Signature).Recv(); receiver != nil {
			t := receiver.Type()
			if pointer, ok := t.(*types.Pointer); ok {
				t = pointer.Elem()
			}
			if named, ok := t.(*types.Named); ok {
				return base + named.Obj().Name() + "." + obj.Name()
			}
			return base + file + "." + obj.Name()
		}
	}
	if v, ok := obj.(*types.Var); ok && v.IsField() {
		if owner := fields[v]; owner != "" {
			return base + owner + "." + obj.Name()
		}
		return base + file + "." + obj.Name()
	}
	return base + obj.Name()
}

type siteVisitor struct {
	context string
	next    *int
	sites   map[*ast.Ident]string
}

func (v siteVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}
	if _, ok := node.(*ast.FuncLit); ok {
		(*v.next)++
		v.context += "/closure" + strconv.Itoa(*v.next)
	}
	if ident, ok := node.(*ast.Ident); ok {
		v.sites[ident] = v.context
	}
	return v
}

func callerSites(files []*ast.File, info *types.Info, fields map[*types.Var]string) map[*ast.Ident]string {
	result := map[*ast.Ident]string{}
	visit := func(node ast.Node, names []*ast.Ident, suffix string) {
		keys := []string{}
		for _, name := range names {
			if obj := info.Defs[name]; obj != nil {
				keys = append(keys, objectKey(obj, "", fields))
			}
		}
		count := 0
		ast.Walk(siteVisitor{strings.Join(keys, ",") + suffix, &count, result}, node)
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				visit(d, []*ast.Ident{d.Name}, "")
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						visit(s, []*ast.Ident{s.Name}, "")
					case *ast.ValueSpec:
						visit(s, s.Names, "")
						if len(s.Values) > 1 {
							for i, value := range s.Values {
								visit(value, s.Names, "/value"+strconv.Itoa(i))
							}
						}
					}
				}
			}
		}
	}
	return result
}

func reachableSafe(graph map[string]map[string]bool, p policy) []string {
	seen := map[string]bool{}
	queue := []string{}
	for caller, destinations := range graph {
		if !p.safe(caller) {
			queue = append(queue, caller)
		}
		for callee := range destinations {
			if !p.safe(callee) {
				queue = append(queue, callee)
			}
		}
	}
	for len(queue) > 0 {
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[current] {
			continue
		}
		seen[current] = true
		for next := range graph[current] {
			queue = append(queue, next)
		}
	}
	result := []string{}
	for file := range seen {
		if p.safe(file) {
			result = append(result, file)
		}
	}
	sort.Strings(result)
	return result
}

func sortManifest(m *manifest) {
	sort.Slice(m.Imports, func(i, j int) bool {
		if m.Imports[i].File != m.Imports[j].File {
			return m.Imports[i].File < m.Imports[j].File
		}
		return m.Imports[i].Path < m.Imports[j].Path
	})
	sort.Slice(m.Edges, func(i, j int) bool {
		a, b := m.Edges[i], m.Edges[j]
		if a.Caller != b.Caller {
			return a.Caller < b.Caller
		}
		if a.CallerObject != b.CallerObject {
			return a.CallerObject < b.CallerObject
		}
		if a.Callee != b.Callee {
			return a.Callee < b.Callee
		}
		return a.Object < b.Object
	})
	sort.Strings(m.FromRun)
	sort.Strings(m.ToRun)
}
