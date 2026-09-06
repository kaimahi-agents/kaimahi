#!/usr/bin/env bash
# Refuse to carry Azure identifiers in the tree.
#
# This repo is public. A committed subscription or tenant GUID
# fingerprints the owner; a committed registry login server or cluster
# FQDN names live infrastructure and invites squatting on the registry
# name. The managed-cluster path is therefore entirely parameterised, and
# this check is what keeps it that way — including while the AKS run is
# being written up, which is exactly when a pasted terminal transcript is
# most likely to carry one.
#
# Scope honestly: these are SHAPE rules. A bare resource-group, cluster or
# WORKSPACE name is just a string and cannot be detected this way — keeping
# those out is the author's job, helped by the fact that every one of them is
# a parameter with no committed default. A workspace name is caught only when
# it appears inside a resource id or a hostname, where it has a shape; written
# on its own it is invisible here and must be redacted by hand, in the tree
# and in anything pasted into a pull request.
#
# What is refused:
#   - GUIDs (subscription and tenant ids are the usual leak)
#   - *.azmk8s.io  (an AKS API-server FQDN is per-cluster and per-tenant)
#   - a LITERAL <name>.azurecr.io — registry login servers must always be
#     built from a variable or an obvious placeholder, never a real name
#   - a LITERAL <label>.<region>.cloudapp.azure.com (the public edge's
#     DNS label names a load balancer someone can later own — same rule,
#     variable or placeholder only)
#   - a LITERAL Azure Monitor workspace or Log Analytics workspace resource
#     id. Observability adds a second workspace kind to this repo's
#     vocabulary, and both have a fixed resource-id shape ending in the
#     workspace NAME. Their workspace ids are GUIDs, already refused above.
#   - a LITERAL *.monitor.azure.com endpoint. The Managed Prometheus query
#     endpoint and a data-collection endpoint both derive their host from the
#     workspace name plus a per-resource suffix, so the hostname identifies
#     the workspace as surely as its id does.
#   - a public IPv4 address (the edge's public IP). Private, loopback,
#     link-local, CGNAT, multicast/reserved, the unspecified/broadcast
#     addresses and the RFC 5737 documentation ranges are not
#     identifiers; a short allowlist of well-known public resolvers
#     (1.1.1.1 is the egress probe's control target) is kept explicit.
#
# Scope: what is actually IN the repo — tracked files plus untracked ones
# git would accept. "In the tree" is the claim being checked, and a
# developer's working directory is full of gitignored run artifacts
# (chat.out, ledger dumps) whose A2A task UUIDs would turn a precise gate
# into noise people learn to ignore. Pass explicit paths to scan those
# instead.
#
# Run:  bash scripts/check-no-azure-ids.sh [path...]
set -euo pipefail

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

if [ "$#" -gt 0 ]; then
  find "$@" -type f -print0 > "$workdir/files"
elif git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git ls-files -z --cached --others --exclude-standard > "$workdir/files"
else
  find . -type f -print0 > "$workdir/files"
fi

# A scanner that examined nothing must not report "clean". An empty list
# means the enumeration failed (or matched nothing), which is a broken
# gate, not a passing one — the same fail-open shape as a grep whose error
# is read as "no match".
if [ ! -s "$workdir/files" ]; then
  echo "check-no-azure-ids: no files to scan — refusing to report a clean tree." >&2
  exit 1
fi

python3 - "$workdir/files" <<'PY'
import re, sys

SKIP_DIRS = {".git", "bin", ".claude", "node_modules"}
SKIP_SUFFIX = {".png", ".jpg", ".gif", ".pdf", ".sha256", ".sum"}

GUID = re.compile(r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
                  r"[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b")
AKS_FQDN = re.compile(r"[A-Za-z0-9-]+\.[a-z0-9-]+\.azmk8s\.io")
# Anything ending in .azurecr.io. A registry reference is only acceptable
# when the name part is a shell/make variable or a visible placeholder.
# \x27 is the apostrophe: written as an escape so this regex carries no
# single quote of its own, which would otherwise have to be escaped out
# of the surrounding shell quoting and become unreadable.
ACR = re.compile(r"(?P<name>[^\s\"\x27`/=]*)\.azurecr\.io")
CLOUDAPP = re.compile(r"(?P<name>[^\s\"\x27`/=]*)\.[a-z0-9<>$(){}_-]*\.cloudapp\.azure\.com")
# The two workspace kinds observability adds. Both resource ids have a fixed
# provider path and end in the workspace name, which is the half a shape rule
# can see. Matched case-insensitively because ARM ids round-trip in whatever
# case they were written.
AMW_ID = re.compile(r"/providers/Microsoft\.Monitor/accounts/(?P<name>[^\s\"\x27`/,)\]]*)", re.I)
LAW_ID = re.compile(r"/providers/Microsoft\.OperationalInsights/workspaces/(?P<name>[^\s\"\x27`/,)\]]*)", re.I)
# Managed Prometheus query endpoints and data-collection endpoints. The
# leading label is derived from the workspace name, so the hostname names the
# workspace. The middle components (region, and 'prometheus' or 'ingest') vary,
# so `name` here can span more than one dot-separated component — which is why
# it is checked with placeholder_path rather than PLACEHOLDER.
MONITOR_ENDPOINT = re.compile(r"(?P<name>[^\s\"\x27`/=]*)\.[a-z0-9<>$(){}_-]*\.monitor\.azure\.com", re.I)
# An ARM template's contentVersion, and only its value. Used to exempt that
# exact span from the IPv4 rule — the key is required to carry four
# dot-separated numbers, which no shape rule can tell from an address.
CONTENT_VERSION = re.compile(r"contentVersion\"?\s*[:=]\s*\"(?P<value>[0-9.]+)\"", re.I)
# Trailing: not a word char, and not ".<digit>" (a fifth component means a
# five-component version string); a sentence-final "." after a real address
# still matches.
IPV4 = re.compile(r"(?<![\w.])(?P<ip>(?:\d{1,3}\.){3}\d{1,3})(?!\w)(?!\.\d)")
WELL_KNOWN_IPS = {"1.1.1.1", "1.0.0.1", "8.8.8.8", "8.8.4.4", "9.9.9.9"}
PLACEHOLDER = re.compile(r"""(
      \$\(?\{?[A-Za-z_][A-Za-z0-9_]*\}?\)?   # $ACR, ${ACR}, $(ACR_NAME)
    | <[^>]*>                                 # <your-registry>
    | ^$                                      # bare ".azurecr.io"
)$""", re.X)

import ipaddress, pathlib

def placeholder_path(name):
    """True when every dot-separated component is a placeholder or empty.

    A monitor endpoint has more components before the fixed suffix than an
    ACR or cloudapp host does, so its parameterised form is a SEQUENCE of
    placeholders (`$AMW.$REGION.prometheus.monitor.azure.com`) rather than a
    single one. Checking each component keeps that form admissible without
    admitting `realname.westus3`, where one component is a literal."""
    return all(PLACEHOLDER.match(part) for part in name.split("."))

def public_ip(s):
    """True for a syntactically valid IPv4 address that could name live
    internet infrastructure. Anything ipaddress classifies as private,
    loopback, link-local, multicast, reserved or unspecified is not an
    identifier; nor are the documentation ranges or the well-known
    resolvers above."""
    try:
        ip = ipaddress.IPv4Address(s)
    except ValueError:
        return False  # 999.1.1.1 and friends: not an address
    if s in WELL_KNOWN_IPS or not ip.is_global or ip.is_multicast:
        return False
    for doc in ("192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"):
        if ip in ipaddress.IPv4Network(doc):
            return False
    return True

findings = []
for raw in open(sys.argv[1], "rb").read().split(b"\0"):
    if not raw:
        continue
    p = pathlib.Path(raw.decode("utf-8", "replace"))
    if p.suffix in SKIP_SUFFIX or SKIP_DIRS & set(p.parts):
        continue
    try:
        text = p.read_text()
    except UnicodeDecodeError:
        continue  # binary; nothing text-shaped to leak
    except OSError as e:
        # Unreadable is not clean. Report it rather than skipping, so a
        # permissions problem cannot quietly shrink the scanned set.
        findings.append((p, 0, "could not be read (not scanned)", str(e)))
        continue
    for n, line in enumerate(text.splitlines(), 1):
        for m in GUID.finditer(line):
            # Obviously-synthetic GUIDs (00000000-...-000000000099 and
            # friends) are test fixtures, not identifiers. A real Azure
            # subscription/tenant id is random, so it never has this
            # little entropy. Keep the exemption tight.
            if len(set(m.group(0).replace("-", ""))) <= 4:
                continue
            findings.append((p, n, "GUID (subscription/tenant id?)", m.group(0)))
        for m in AKS_FQDN.finditer(line):
            findings.append((p, n, "AKS cluster FQDN", m.group(0)))
        for m in ACR.finditer(line):
            if not PLACEHOLDER.match(m.group("name")):
                findings.append((p, n, "literal ACR login server", m.group(0)))
        for m in CLOUDAPP.finditer(line):
            if not PLACEHOLDER.match(m.group("name").lstrip("(")):
                findings.append((p, n, "literal public DNS label (cloudapp.azure.com)", m.group(0)))
        for m in AMW_ID.finditer(line):
            if not PLACEHOLDER.match(m.group("name")):
                findings.append((p, n, "literal Azure Monitor workspace resource id", m.group(0)))
        for m in LAW_ID.finditer(line):
            if not PLACEHOLDER.match(m.group("name")):
                findings.append((p, n, "literal Log Analytics workspace resource id", m.group(0)))
        for m in MONITOR_ENDPOINT.finditer(line):
            if not placeholder_path(m.group("name")):
                findings.append((p, n, "literal Azure Monitor endpoint (monitor.azure.com)", m.group(0)))
        # An ARM template's contentVersion is required to be four
        # dot-separated numbers, which is indistinguishable from an address
        # by shape alone. The exemption covers only the SPAN of that value,
        # not the line: exempting the whole line would let a real address
        # sitting beside a contentVersion key pass unseen, which is a gate
        # that can be walked around by writing two things on one line.
        version_spans = [m.span("value") for m in CONTENT_VERSION.finditer(line)]
        for m in IPV4.finditer(line):
            if any(start <= m.start("ip") and m.end("ip") <= end for start, end in version_spans):
                continue
            if public_ip(m.group("ip")):
                findings.append((p, n, "public IPv4 address", m.group(0)))

for path, n, why, what in findings:
    print(f"{path}:{n}: {why}: {what}")
if findings:
    print(f"\n{len(findings)} Azure identifier(s) in the tree — this repo is public.")
    print("Parameterise them (env vars / <placeholders>) and redact pasted evidence.")
    sys.exit(1)
print("no Azure identifiers in the tree")
PY
