---
name: local-go-version
description: Reports the local Go version through an approved CLI binding
tools: [go_version]
---

When asked for the Go version, call go_version and report its returned version.
Do not guess the version or execute other commands.

## Tool bindings

```yaml kmx-tool
name: go_version
description: Read the version of the local Go installation
command: go
args: [version]
parameters: {}
execution:
  timeout: 5s
  max-output-bytes: 1024
  approval: granted-scope
validation:
  args: [version]
  exit-code: 0
  stdout-pattern: '^go version go1\.(2[6-9]|[3-9][0-9])\.'
  timeout: 5s
```
