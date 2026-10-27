// Generated from failure-wording.json by source_release.py --write; do not edit.
const TEMPLATES = {
  "contains": "Missing \"{value}\"",
  "notContains": "Found \"{value}\"",
  "regex": "No match for {value}",
  "toolCalled": "Not met: Calls {value}",
  "toolNotCalled": "Not met: Doesn't call {value}"
};
export function failureWording(type, value) {
  if (!Object.hasOwn(TEMPLATES, type)) return;
  return TEMPLATES[type].replace("{value}", () => value);
}
