package localtools

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckJSONInspectsNestedEncodedStrings(t *testing.T) {
	refused := errors.New("refused")
	check := func(values ...string) error {
		for _, v := range values {
			if strings.Contains(v, "private-canary") {
				return refused
			}
		}
		return nil
	}
	for _, value := range []any{
		map[string]any{"result": `{"text":"\u0070rivate-canary"}`},
		map[string]any{"result": `{"\u0070rivate-canary":"safe"}`},
	} {
		if err := CheckJSON(value, check); !errors.Is(err, refused) {
			t.Fatalf("encoded credential unchecked: %v", err)
		}
	}
}
