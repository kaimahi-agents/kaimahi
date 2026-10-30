package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const emptyMarkdownTools = "schemaVersion: 1\ntools: {}\n# Add named command bindings here; approval is machine-local.\n"
const emptyMarkdownTests = "---\nschemaVersion: 1\n---\n\n# Tests\n\nNo tests defined yet. Add reviewed yaml agent-test blocks here.\n"

// ImportMarkdownDirectory copies a frozen source and its explicit companions into
// one new directory. Neither imports nor copied declarations grant execution.
func ImportMarkdownDirectory(ctx context.Context, source, destination string) (string, error) {
	a, err := ReadMarkdownAgent(source)
	if err != nil {
		return "", err
	}
	if destination == "" {
		return "", errors.New("import requires --into DIRECTORY")
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("import destination already exists or is unavailable")
	}
	files := map[string][]byte{filepath.Base(a.Path): a.Source}
	root := filepath.Dir(a.Path)
	for _, sources := range []map[string][]byte{a.ToolSources, a.TestSources} {
		for path, b := range sources {
			rel, err := filepath.Rel(root, path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", errors.New("import companion must be inside agent directory")
			}
			if _, ok := files[rel]; ok {
				return "", errors.New("import file collision")
			}
			files[rel] = b
		}
	}
	if len(a.ToolSources) == 0 {
		files[filepath.Base(MarkdownToolsPath(a.Path))] = []byte(emptyMarkdownTools)
	}
	if len(a.TestSources) == 0 {
		files[filepath.Base(MarkdownTestsPath(a.Path))] = []byte(emptyMarkdownTests)
	}
	if err := a.CheckCurrent(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Mkdir(destination, 0755); err != nil {
		return "", err
	}
	for _, rel := range sortedMarkdownPaths(files) {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("import incomplete in %s: %w", destination, err)
		}
		path := filepath.Join(destination, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return "", err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return "", err
		}
		_, writeErr := f.Write(files[rel])
		err = errors.Join(writeErr, f.Close())
		if err != nil {
			return "", err
		}
	}
	if err := a.CheckCurrent(); err != nil {
		return "", fmt.Errorf("source changed; imported snapshot remains at %s: %w", destination, err)
	}
	return filepath.Join(destination, filepath.Base(a.Path)), nil
}

// ImportMarkdownAgent is the terminal convenience wrapper over directory import.
func ImportMarkdownAgent(ctx context.Context, path string, in io.Reader, out io.Writer) (string, error) {
	source, err := ResolveMarkdownSource(path)
	if err != nil {
		return "", err
	}
	destination := markdownStem(source)
	if _, err = fmt.Fprintf(out, "Import unchanged agent and companions into %s? [y/N]: ", safeTerminal(destination)); err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(mdImportLineReader{in})
	if !scanner.Scan() || !strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
		return "", errors.New("import cancelled; source unchanged")
	}
	saved, err := ImportMarkdownDirectory(ctx, source, destination)
	if err == nil {
		_, err = fmt.Fprintln(out, "Imported agent:", safeTerminal(saved))
	}
	return saved, err
}

type mdImportLineReader struct{ io.Reader }

func (r mdImportLineReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return r.Reader.Read(p[:1])
}
