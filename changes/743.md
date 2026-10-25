### Added

- Agent canvas Test rows update as cases finish through native tool progress. Stop Copilot turn cancels the whole chat turn and keeps partial rows in the open canvas.
- The canvas retains two models' results for row-by-row comparison. Picking a previously tested model restores its current result without another run; newer partial attempts take precedence over older full runs. Compare and stopped-row memory lasts only while the canvas is open.
