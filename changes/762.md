### Fixed

- File tests with tools now run on Copilot models that add reasoning to their answers, such as Claude Haiku 5.5. A reply kmx cannot use now says so, instead of only "could not finish".
- In the agent canvas, a model that doesn't finish no longer locks the next run as "Result unconfirmed". It is not picked again as the comparison model, and a comparison model that doesn't finish shows its own result.
- After a run records tool calls, the test shows that run's result and "Recording saved." instead of "Not run yet". Enter on a focused Live/Recorded switch saves and runs the test.
