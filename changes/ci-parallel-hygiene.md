### Changed

- Run hygiene checks alongside the App and Windows tests instead of after them. The required `hygiene` gate still requires every check and native test job to succeed, including on documentation-only changes; required check names, platform tests and timeouts are unchanged.
