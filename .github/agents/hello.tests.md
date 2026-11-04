---
schemaVersion: 1
---

# Hello tests

## Greeting Sam

```yaml agent-test
id: greet-sam
input: My name is Sam
assertions:
  - id: greeting
    type: regex
    pattern: '^Hello, Sam!$'
```

## Greeting Aria

```yaml agent-test
id: greet-aria
input: My name is Aria
assertions:
  - id: greeting
    type: regex
    pattern: '^Hello, Aria!$'
```
