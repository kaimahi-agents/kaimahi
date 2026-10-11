package agentsuite

import "golang.org/x/text/cases"

func foldCase(value string) string {
	return cases.Fold().String(value)
}
