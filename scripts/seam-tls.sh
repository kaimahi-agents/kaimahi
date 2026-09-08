#!/usr/bin/env bash
# Shared by the probes that reach one of the plane's two data seams on a
# CLUSTER. plane-upgrade-probe.sh is the exception and always will be: it runs
# the plane as a local process, so it mints its own material with openssl
# rather than reading a Secret that does not exist.
#
# Both seams serve TLS under the plane's own certificate authority, which no
# system trust store has heard of, so a probe has to be told what to verify
# against. This fetches the authority's CERTIFICATE — public material naming
# who to trust, never the private key, which stays in the kaimahi namespace
# and is mounted into no pod.
#
# Verification is not optional here and `--insecure` must not appear in any
# probe. A probe that skipped verification would keep passing on the day the
# certificate expired or stopped matching, which is exactly the failure these
# probes exist to catch, and it would pass while the seam it is proving was
# unreachable to every real client.
#
# A probe reaches a seam through `kubectl port-forward` to 127.0.0.1, so it
# verifies against the certificate's loopback address rather than a Service
# name. That is a real SAN on a real certificate, not a relaxation: the plane
# dials its own seams the same way, for its liveness probe and its approval
# notifier.

# seam_ca <path> [namespace]
#
# Writes the authority's certificate to <path>. Fails loudly rather than
# leaving an empty file: curl treats an unreadable --cacert as a hard error,
# but an EMPTY one produces a verification failure that reads like a bad
# certificate on the server.
seam_ca() {
  local out=$1 ns=${2:-${NAMESPACE:-kaimahi}}
  # shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
  $KUBECTL -n "$ns" get secret kaimahi-plane-seam-tls \
    -o jsonpath='{.data.ca\.crt}' | base64 -d > "$out"
  if [ ! -s "$out" ]; then
    echo "the plane has no seam certificate authority in $ns/kaimahi-plane-seam-tls" >&2
    echo "  (run 'kmx plane' — it mints the certificate and publishes the authority)" >&2
    return 1
  fi
}
