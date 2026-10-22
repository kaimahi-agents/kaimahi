//go:build !windows

package localpreview

import "testing"

func TestJournalPathWithQuestionMark(t *testing.T) {
	testJournalPath(t, "a dir #1 %20 ?x")
}
