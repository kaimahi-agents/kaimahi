// Command comment-history-go reports Go comments using the Go parser.
package main

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

type comment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func main() {
	if len(os.Args) < 3 || os.Args[1] != "--" {
		fmt.Fprintln(os.Stderr, "usage: comment-history-go -- file.go [file.go ...]")
		os.Exit(2)
	}
	found := []comment{}
	for _, path := range os.Args[2:] {
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, path, nil, parser.ParseComments)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, group := range file.Comments {
			for _, c := range group.List {
				for offset, line := range strings.Split(c.Text, "\n") {
					found = append(found, comment{path, set.Position(c.Pos()).Line + offset, line})
				}
			}
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(found); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
