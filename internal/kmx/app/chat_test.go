package app

import "testing"

// The controller's real transport failures, verbatim from the runs that
// reddened CI (the Makefile's comments record both).
const (
	refusedLine   = `Error invoking session: failed to send task: failed to send HTTP request: Post "http://hello-world.kagent:8080": dial tcp 10.96.12.3:8080: connect: connection refused`
	eofLine       = `Error invoking session: failed to send task: failed to send HTTP request: Post "http://hello-world.kagent:8080": EOF`
	resetLine     = `Error invoking session: failed to send task: failed to send HTTP request: Post "http://hello-world.kagent:8080": read tcp 127.0.0.1:5555->127.0.0.1:8080: read: connection reset by peer`
	unrelatedLine = `Error invoking session: failed to send task: failed to send HTTP request: Post "http://hello-world.kagent:8080": context deadline exceeded`
)

func TestRetryClassesMatchTheControllersErrors(t *testing.T) {
	if !ChatRetryable.MatchString(refusedLine) {
		t.Error("a refused dial must be retried")
	}
	if !ChatRetryable.MatchString(eofLine) || !ChatRetryable.MatchString(resetLine) {
		t.Error("EOF and connection reset must be retried by chat")
	}
	if ChatRetryable.MatchString(unrelatedLine) {
		t.Error("an unrelated transport error must NOT be retried")
	}
}

// The output being matched is stdout+stderr, and stdout carries the A2A task
// JSON — including the model's own words. An agent asked to explain one of
// these errors (the FAQ documents them) would echo the text; a loose match
// would then trigger a second invoke: duplicate spend, and for a tool call a
// burned grant.
func TestAModelEchoingTheErrorDoesNotTriggerARetry(t *testing.T) {
	for _, reply := range []string{
		`{"result":{"status":{"message":{"parts":[{"text":"That message means: Error invoking session: failed to send HTTP request: Post \"http://x\": EOF"}]}}}}`,
		`  Error invoking session: failed to send HTTP request: Post "http://x": EOF`,
		`The agent said: dial tcp 10.0.0.1:8080: connect: connection refused`,
		`Error invoking session: failed to send HTTP request: Post "http://x": EOF happened, then it recovered`,
	} {
		if ChatRetryable.MatchString(reply) {
			t.Errorf("a reply that merely mentions the error must not be retried:\n%s", reply)
		}
	}
}

// A real failure inside a multi-line capture still matches: the anchors are
// per line, not per output.
func TestARealFailureIsFoundInAMultiLineCapture(t *testing.T) {
	out := "some preamble\n" + refusedLine + "\n"
	if !ChatRetryable.MatchString(out) {
		t.Error("the error line must be found anywhere in the captured output")
	}
}

func TestForwardedPort(t *testing.T) {
	for _, tc := range []struct {
		output, want string
		ok           bool
	}{
		{"Forwarding from 127.0.0.1:41783 -> 8083\n", "41783", true},
		{"Forwarding from [::1]:41783 -> 8083\n", "", false},
		{"Unable to listen on port 8083", "", false},
	} {
		got, err := forwardedPort(tc.output)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("forwardedPort(%q)=(%q,%v), want (%q,ok=%v)", tc.output, got, err, tc.want, tc.ok)
		}
	}
}
