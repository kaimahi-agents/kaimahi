### Fixed

- Reusing a native offline Orka install no longer refuses with "offline live workload set differs" after `kmx model connect` added its model gateway Deployment to `orka-system`. Deployments carrying kmx's model operation marker are left out of the bundle's workload set; every bundle Deployment is still compared exactly, even when marked.
