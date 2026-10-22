// Package secretshapes provides shared credential-pattern checks from embedded rules.
// Go code and repository checks use the same rules in shapes.json.
// These checks recognize known shapes; they do not prove that text is secret-free.
package secretshapes

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

//go:embed shapes.json
var raw []byte

// Shape is one credential shape.
type Shape struct {
	// Name is the stable identifier, used by tests and by the tree scan's
	// output. Not shown to an operator.
	Name string
	// What names the credential in a refusal: "a GitHub token".
	What string
	// Why says who issues it and how it reaches a file.
	Why string
	// Re matches it.
	Re *regexp.Regexp
	// Example is a value this shape must match, and Counterexample one it
	// must not. Both are assembled from parts held in shapes.json so the
	// file carries no credential-shaped literal of its own.
	Example, Counterexample string
}

type fileShape struct {
	Name           string   `json:"name"`
	What           string   `json:"what"`
	Why            string   `json:"why"`
	Regexp         string   `json:"regexp"`
	Example        []string `json:"example"`
	Counterexample []string `json:"counterexample"`
}

var shapes []Shape

func init() {
	var doc struct {
		Shapes []fileShape `json:"shapes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		panic("secretshapes: shapes.json does not parse: " + err.Error())
	}
	// A list that ended up empty is a list that stopped guarding anything,
	// and the failure would otherwise be silent: every consumer would keep
	// reporting a clean document. Refuse at load, in the process that was
	// about to trust it.
	if len(doc.Shapes) == 0 {
		panic("secretshapes: shapes.json declares no shapes — nothing would be refused anywhere")
	}
	seen := map[string]bool{}
	for _, s := range doc.Shapes {
		if s.Name == "" || s.What == "" || s.Regexp == "" || len(s.Example) == 0 {
			panic(fmt.Sprintf("secretshapes: shape %q is missing a name, what, regexp or example", s.Name))
		}
		if seen[s.Name] {
			panic("secretshapes: two shapes are named " + s.Name)
		}
		seen[s.Name] = true
		re, err := regexp.Compile(s.Regexp)
		if err != nil {
			panic(fmt.Sprintf("secretshapes: shape %q does not compile: %v", s.Name, err))
		}
		shapes = append(shapes, Shape{
			Name: s.Name, What: s.What, Why: s.Why, Re: re,
			Example:        strings.Join(s.Example, ""),
			Counterexample: strings.Join(s.Counterexample, ""),
		})
	}
}

// All returns every shape, in the order shapes.json declares them.
func All() []Shape {
	out := make([]Shape, len(shapes))
	copy(out, shapes)
	return out
}

// Match returns the first shape the text contains, and nil when it
// contains none.
func Match(text string) *Shape {
	for i := range shapes {
		if shapes[i].Re.MatchString(text) {
			return &shapes[i]
		}
	}
	return nil
}

// MatchIndex is Match with the position of the match, for a caller that
// reports where in a document the value sits rather than what it was.
func MatchIndex(text string) (*Shape, int) {
	for i := range shapes {
		if loc := shapes[i].Re.FindStringIndex(text); loc != nil {
			return &shapes[i], loc[0]
		}
	}
	return nil, -1
}
