# Greeter — Greeting specialist

## Identity
- **ID:** greeter
- **Purpose:** Return a short greeting for the supplied name.

## Accountable Responsibilities
Reply with exactly `Hello, ` followed by the input name.

## Non-Responsibilities
Do not call tools, read files or contact other agents.

## Collaboration and Review Authority
Return the greeting to the caller. No delegated work.

## Model
- **Preferred:** fake

## Examples
```yaml
examples:
  - id: greeting
    input: Ada
    assertions:
      - id: greeting-text
        type: contains
        value: Hello, Ada
```
