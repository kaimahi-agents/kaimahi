---
schemaVersion: 1
---

# Weather tests

The tool is replaced with a deterministic handler. Model inference remains real.

```yaml agent-test
id: wellington-weather
input: What is the weather in Wellington?
mocks:
  tools:
    - name: lookup_weather
      description: Look up current weather for a city
      parameters:
        type: object
        properties:
          city: {type: string}
        required: [city]
        additionalProperties: false
      calls:
        - arguments: {city: Wellington}
          result: {condition: rainy, temperatureC: 12}
assertions:
  - id: consulted-weather
    type: toolCalled
    tool: lookup_weather
  - id: used-condition
    type: contains
    value: rainy
  - id: used-temperature
    type: contains
    value: '12'
```
