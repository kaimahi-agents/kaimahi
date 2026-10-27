// Authored examples for the recording; model inference happens only through Test.
export const FIELD_SERVICE_BRIEF = "Add support for working without internet to our field-service app. Technicians must keep recording jobs, preserve local changes, and synchronize safely when the connection returns.";
export const STORAGE_PROMPT = "Design client-side storage for a field-service app used without internet. Return only a compact JSON object with exactly two keys: storage and reconnect. Each value must be a string of one sentence. Explain durable local storage in storage and idempotent replay in reconnect. Do not include additional keys or nested objects.";
export const STORAGE_FIXED_PROMPT = "Design client-side storage for a field-service app used without internet. Return only a compact JSON object with exactly three keys: storage, reconnect, and conflictStrategy. Each value must be a string. Use one sentence each for durable local storage and idempotent replay. The conflictStrategy string must be exactly \"preserve-both\", meaning both conflicting local and server versions are retained for user resolution. Do not use nested objects.";

export function fieldServiceProposal(model) {
  const make = (memberId, role, taskInstructions, cases) => ({ memberId, role, displayRole: role, taskInstructions, model,
    tests: cases.map(([id, input, pattern]) => ({ id, input, assertions: [{ id: "required-behavior", type: "regex", pattern }] })),
  });
  const members = [
    make("lead", "lead", "You are the technical lead for a field-service application. Give a concise implementation plan covering durable local storage, synchronization after reconnecting, and conflict resolution. Explain boundaries between the storage, synchronization and testing roles.", [["covers-storage", "Plan the feature for technicians working without connectivity.", "(?i)storage"], ["covers-sync", "Plan recovery when the connection returns.", "(?i)synchron"]]),
    make("offline-storage", "Offline storage", STORAGE_PROMPT, [["preserves-writes", "A technician saves a job without connectivity. Return the storage design contract.", '"storage"\\s*:'], ["avoids-loss", "Local and server edits conflict after reconnecting. Return the storage design contract.", '"conflictStrategy"\\s*:\\s*"preserve-both"']]),
    make("sync", "Synchronization", "Design synchronization for a field-service app. Explain idempotent replay, retries after disconnects and preserving both versions of conflicting edits. Answer the supplied scenario concisely.", [["safe-retry", "The connection drops halfway through sending local edits. Explain recovery.", "(?i)idempotent"], ["conflict-safety", "Two technicians edit the same job. Explain how to avoid data loss.", "(?i)(both|versions)"]]),
    make("quality", "Quality / testing", "Design tests for field-service app operation without internet. Include airplane mode, reconnect, duplicate replay, and conflict recovery where relevant to the scenario.", [["airplane-mode", "How do we test use without connectivity?", "(?i)airplane mode"], ["recovery", "How do we test conflicting updates on reconnect?", "(?i)conflict"]]),
  ];
  members[0].displayRole = "Lead / Architect";
  return { brief: FIELD_SERVICE_BRIEF, lead: members[0], specialists: members.slice(1) };
}
