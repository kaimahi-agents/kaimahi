---
schemaVersion: 1
---

# Local CLI response handling (mocked; no binary is executed)

```yaml agent-test
id: reports-version
input: What Go version is installed?
mocks:
  tools:
    - name: go_version
      description: Read the local Go version
      parameters:
        type: object
        properties: {}
        additionalProperties: false
      calls:
        - arguments: {}
          result: go version go1.26.10 linux/amd64
assertions:
  - id: consulted-local-tool
    type: toolCalled
    tool: go_version
  - id: reported-version
    type: contains
    value: 1.26.10
```
