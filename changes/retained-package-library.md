### Added

- Internal retained-package access verifies saved bytes and source freshness under an exclusive lock after restart, supports bounded offline inspection, and reclaims only owned entries. List keeps damaged entries visible as unavailable without exposing their metadata. This library adds no commands, passing-test evidence or publication capability; native Windows retained packages remain unsupported.
