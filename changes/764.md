### Fixed

- While a save waits for Copilot approval before a canvas test, the canvas says "Copilot approval is waiting in chat." instead of only "Saving and running".
- Tests, checks, tool modes and recordings added in the canvas are written as ordinary block YAML, not one-line JSON. Layouts that already use flow style keep it.
- A test name typed in the canvas keeps its capitals ("CI") after saving and reloading.
- `/kmx-canvas open PATH` resolves a relative path in the session folder, says "File not found" when the file is missing, and confirms when the canvas opens.
