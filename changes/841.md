### Added

- Internal tool-free Orka compiler for frozen mapped inputs, retaining exact agent and companion source bytes in the closed `orka-source/v1` OCI profile. Compilation shares the existing native resolver and packager; local transfer copies verified bytes without replacing an occupied destination.

### Upgrading

- Source-bearing packages require a supporting native reader; older readers refuse them. Existing workspace package digests and receipts stay unchanged. This library foundation provides no file Prepare command, retained handle or handle-mode Test/Push capability. Source content is non-confidential; bounded OS-temp copies may survive a crash.
