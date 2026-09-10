package convert

import "strings"

// Frozen Orka v0.1.3 inventory: internal/tools/registry.go's
// RegisterBuiltinTools, ChatToolNames, and CoordinationToolNames, resolving
// names in common_constants.go and the corresponding tool Name methods.
// This includes all proxy/chat and worker-only built-ins, not just defaults.
const builtInNames = `
auto_merge_pull_request
cancel_task
check_messages
check_pr_review_marker
check_pull_request_ci
check_task_progress
code_exec
comment_on_issue
create_agent
create_agent_task
create_ai_task
create_container_task
create_pr_monitor
create_pull_request
create_tool
delegate_task
delete_agent
delete_session
delete_tool
fetch_task_output
file_read
file_write
get_issue
list_agents
list_issues
list_pull_requests
list_tasks
list_tools
merge_pull_request
post_review_comment
propose_memory
recall_memory
remember
request_approval
review_pull_request
search_transcript
send_message
update_agent
update_plan
wait_for_task
wait_for_tasks
web_fetch
web_search
`

func isBuiltIn(name string) bool {
	for _, builtIn := range strings.Fields(builtInNames) {
		if name == builtIn {
			return true
		}
	}
	return false
}
