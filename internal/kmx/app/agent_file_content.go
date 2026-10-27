package app

import (
	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func previewFullAnswer(answer string) *string {
	text, safe := suiteDiagnosticText(answer, len(answer), nil)
	if !safe {
		text = "[content withheld]"
	}
	return &text
}

func previewCheckFailure(a runtime.EvaluationAssertion) string {
	value := a.Value
	switch a.Type {
	case "regex":
		value = a.Pattern
	case "toolCalled", "toolNotCalled":
		value = a.Tool
	}
	safe, ok := suiteDiagnosticText(value, 4096, nil)
	if !ok {
		return "[check content withheld]"
	}
	return kaimahi.FileCheckFailure(a.Type, safe)
}
