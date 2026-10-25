### Changed

- File Run/Test supports up to five tool calls in the first model response, executed in order before one final-answer request. Both native host tools and the fixed binding keep their existing permissions, deadlines, size bounds and pre-journal checks. Older zero/one-call journals remain inspectable; no adaptive tool rounds, new approval flow or live-tool allowlist is added (#698).
