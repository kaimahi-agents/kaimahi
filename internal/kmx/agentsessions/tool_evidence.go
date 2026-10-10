package agentsessions

import (
	v1 "github.com/aramase/agentsessions/api/genpb"
	"google.golang.org/protobuf/proto"
)

const maxPendingToolCalls = 256

// toolEvidenceCollector projects only names/counts from validated journal records.
// Intent is not invocation: the pinned controller appends TOOL_CALL before executing
// and owns the subsequent TOOL_RESULT correlation, including logical error results.
// Completeness is evidence only; it does not establish runtime tool capability.
type toolEvidenceCollector struct {
	executionID    string
	records, bytes int
	unavailable    bool
	pending        map[string]string
	seen           map[string]struct{}
	calls          map[string]int
}

// Reserve before conversion, hashing or retaining correlation state. Overflow
// disables only this projection; RunCase still validates/drains the text journal.
func (c *toolEvidenceCollector) reserve(record *v1.LogRecord) bool {
	if c.unavailable {
		return false
	}
	size := proto.Size(record)
	if c.records >= maxVerifyRecords || size > maxVerifyBytes-c.bytes {
		c.invalidate()
		return false
	}
	c.records++
	c.bytes += size
	return true
}

func (c *toolEvidenceCollector) invalidate() {
	c.unavailable = true
	c.executionID = ""
	c.pending = nil
	c.seen = nil
	c.calls = nil
}

func (c *toolEvidenceCollector) observe(event *v1.Event) {
	if c.unavailable {
		return
	}
	if event == nil || !safeIdentity(event.GetExecutionId()) || c.executionID != "" && event.GetExecutionId() != c.executionID {
		c.invalidate()
		return
	}
	c.executionID = event.GetExecutionId()
	switch event.Kind {
	case v1.EventKind_EVENT_TOOL_CALL:
		call := event.GetTool()
		if call == nil || !safeIdentity(call.GetId()) || !safeIdentity(call.GetTool()) || call.GetMediation() != v1.Mediation_MEDIATION_CONTROLLER_MEDIATED || call.GetIdempotencyKey() == "" {
			c.invalidate()
			return
		}
		if _, duplicate := c.seen[call.GetId()]; duplicate || len(c.pending) >= maxPendingToolCalls {
			c.invalidate()
			return
		}
		if c.pending == nil {
			c.pending = make(map[string]string)
			c.seen = make(map[string]struct{})
		}
		c.pending[call.GetId()] = call.GetTool()
		c.seen[call.GetId()] = struct{}{}
	case v1.EventKind_EVENT_TOOL_RESULT:
		result := event.GetResult()
		name, found := c.pending[result.GetId()]
		if result == nil || !found {
			c.invalidate()
			return
		}
		delete(c.pending, result.GetId())
		if c.calls == nil {
			c.calls = make(map[string]int)
		}
		c.calls[name]++
	case v1.EventKind_EVENT_OUTPUT:
		// Model-produced structured data may contain proposals, not host invocation
		// proof. Do not interpret or retain it, nor claim complete absence from it.
		for _, part := range event.GetMessage().GetParts() {
			if part.GetData() != nil {
				c.invalidate()
				return
			}
		}
	}
}

// Only a fully validated, successful RunCase may publish the projection. All
// other paths discard counts as well as pending/seen IDs, never exposing a prefix.
func (c *toolEvidenceCollector) finish(success bool) (map[string]int, bool) {
	if !success || c.unavailable || len(c.pending) != 0 {
		c.invalidate()
		return nil, false
	}
	calls := c.calls
	if calls == nil {
		// A complete empty map proves zero observed invocations; nil denotes
		// unavailable evidence to the assertion consumer.
		calls = make(map[string]int)
	}
	c.pending = nil
	c.seen = nil
	c.calls = nil
	c.executionID = ""
	return calls, true
}
