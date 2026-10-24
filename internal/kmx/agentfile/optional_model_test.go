package agentfile

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExportStillRequiresAuthoredModel(t *testing.T) {
	source := strings.Replace(sample(t), "## Model\n- **Preferred:** fake\n\n", "", 1)
	filename := exportSource(t, source)
	data, err := ExportRunProfile(filename)
	var detail *Error
	if data != nil || !errors.As(err, &detail) || detail.Field != "Model.Preferred" {
		t.Fatalf("export invented a runtime model: dataPresent=%t error=%v", data != nil, err)
	}
}

func TestOmittedModelPreservesAuthoredSource(t *testing.T) {
	source := strings.Replace(sample(t), "## Model\n- **Preferred:** fake\n\n", "", 1)
	for name, parse := range map[string]func(string, []byte) (*Agent, error){"parse": Parse, "inspect": Inspect} {
		t.Run(name, func(t *testing.T) {
			a, err := parse("greeter.agent.md", []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			if a.Model != "" || a.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(source))) {
				t.Fatal("runtime model was inserted into authored source identity")
			}
			if a.Instructions != strings.Split(source, "## Examples")[0] || len(a.Examples) != 1 {
				t.Fatal("omission changed instructions/examples")
			}
		})
	}
}
