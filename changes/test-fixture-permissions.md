### Fixed

- App-test shards establish a deterministic child umask, so group-writable shell
  defaults do not invalidate ordinary private-storage fixtures. Production
  permission refusals and explicit unsafe-permission tests remain enforced.
- Suite build tests copy reviewed fixture bytes into test-owned trees with
  declared modes rather than inheriting checkout permissions.
