### Added

- `kmx cluster up --step cluster --offline-bundle DIR` creates or reuses a kind cluster from a locally cached, independently pinned node image without provisioning tools or running model/Orka setup. The node-only `kmx-offline/v1` profile verifies local OCI metadata, binary identity and actual engine image/platform/rootfs before mutation. Existing online behavior is unchanged.

### Upgrading

- This profile does not install Orka or import archives. Node creation and exact reuse were walked on Linux amd64 with classic Docker 29.9.2, kind 0.33.0, kubectl 1.35.1 and node v1.35.0. Blocked-egress creation, Podman, Docker's containerd image store and native arm64 execution remain unverified; metadata pins are not runtime qualification. The guest owns cache import and lifetime.
