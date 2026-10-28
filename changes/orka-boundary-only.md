### Changed

- Limit the Orka caller hygiene manifest to typed references across its selected-file boundary. Ordinary imports, safe-to-safe helpers and new calling declarations no longer need manifest edits. Keep initialization checks and review imports that register shared HTTP handlers.
