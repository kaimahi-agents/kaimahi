### Changed

- Release builds start without waiting for Orka or kagent. Publication still requires checked binaries, hermetic inner-loop contracts and native plugin installation checks. Full cluster runtime qualification stays nightly on main and is available through `runtime-qualification.yml` before runtime-related tags; a release tag no longer certifies those runtimes.
