---
schemaVersion: 1
---

# PR monitor fixture

Tests are independent of live credentials and grant no repository access.

```yaml agent-test
id: failing-check
input: Read PR 42 and summarize the failing checks.
mocks:
  tools:
    - name: github_pr_read
      description: Read PR metadata and checks in kaimahi-agents/kaimahi
      parameters:
        type: object
        properties:
          number: {type: integer, minimum: 1}
        required: [number]
        additionalProperties: false
      calls:
        - arguments: {number: 42}
          result:
            repository: kaimahi-agents/kaimahi
            pullRequest: {number: 42, title: Fix greeting}
            checks:
              - {name: unit-tests, status: completed, conclusion: failure}
    - name: github_pr_list
      description: List open pull requests in kaimahi-agents/kaimahi
      parameters:
        type: object
        properties: {}
        additionalProperties: false
      calls: []
assertions:
  - id: read-pr
    type: toolCalled
    tool: github_pr_read
  - id: reports-failure
    type: contains
    value: unit-tests
```
