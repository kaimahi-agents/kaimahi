// Package residenttools owns the private resident-agent tool broker protocol.
// The model worker receives only a typed broker channel and secret-free tool
// definitions; fixed host commands and their process authority remain in the
// separate broker process. The bounded-process backend is guarded execution,
// not a filesystem, network or operating-system sandbox. Additional backends,
// including container isolation, must preserve this protocol and report their
// actual backend rather than silently weakening isolation.
package residenttools
