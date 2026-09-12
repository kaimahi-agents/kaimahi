#!/usr/bin/env python3
"""Every COMMITTED ModelConfig pointing at the model seam must use https
AND name the authority to verify it against.

The interactive ModelConfig `kmx govern` applies is generated rather than
committed and is held to the same rule by Go tests. Raw MCP inventory is
outside this model-only check.

Why this check exists rather than trusting admission. kagent refuses neither
mistake:

  * A ModelConfig with an `https://` baseUrl and no `spec.tls` is admitted.
    The agent then verifies against the system trust store, which has never
    heard of the plane, and every call fails — as a generic connection error,
    because the certificate-naming diagnostic in kagent's own runtime is not
    called on that path.
  * A ModelConfig with a `spec.tls` block beside an `http://` baseUrl is also
    admitted. Nothing fails. The TLS block is simply inert, the seam is
    plaintext, and the manifest looks exactly like one that is not.

The second is the dangerous one: it is a silent downgrade that reads as
configured. Both halves are checked here rather than relying on admission.

Run with --selftest to check the checker against deliberately broken input.
"""
import pathlib
import sys

try:
    import yaml
except ImportError:  # pragma: no cover - CI installs PyYAML
    print("PyYAML is required", file=sys.stderr)
    raise SystemExit(2)

ROOT = pathlib.Path(__file__).resolve().parent.parent

# The model Service, whichever of the four DNS forms a client uses.
SEAM_SERVICES = ("kaimahi-proxy",)

# The namespace the plane's Services live in. A host whose first label
# matches but whose namespace does not is a different endpoint.
PLANE_NAMESPACE = "kaimahi"

# What a seam-facing manifest must name, matching internal/kmx/config.
CA_SECRET = "kaimahi-plane-ca"
CA_KEY = "ca.crt"


def seam_url(url):
    """Is this URL one of the plane's data seams?

    All FOUR DNS forms count, including the bare one-label service name. That
    is not hypothetical tidiness: internal/kmx/seamcert mints a SAN for it, so
    the repository treats it as a real way to address a seam, and a recogniser
    that missed it would wave through a manifest reaching the seam in the
    clear.

    A host outside the plane's namespace is somebody else's endpoint, even
    when the first label matches — `kaimahi-proxy.other-ns` is not this seam.
    """
    if not isinstance(url, str) or "://" not in url:
        return False
    host = url.split("://", 1)[1].split("/", 1)[0].split(":", 1)[0]
    labels = host.split(".")
    if labels[0] not in SEAM_SERVICES:
        return False
    return len(labels) == 1 or labels[1] == PLANE_NAMESPACE


def urls_in(spec):
    """Every baseUrl / url in a spec, wherever it is nested."""
    found = []

    def walk(value):
        if isinstance(value, dict):
            for key, child in value.items():
                if key in ("baseUrl", "base_url", "url") and isinstance(child, str):
                    found.append(child)
                else:
                    walk(child)
        elif isinstance(value, list):
            for child in value:
                walk(child)

    walk(spec)
    return found


def check_document(where, doc):
    """Return a list of complaints about one YAML document."""
    if not isinstance(doc, dict):
        return []
    kind = doc.get("kind")
    if kind != "ModelConfig":
        return []
    spec = doc.get("spec") or {}
    name = (doc.get("metadata") or {}).get("name", "?")
    tls = spec.get("tls")
    seams = [u for u in urls_in(spec) if seam_url(u)]
    problems = []

    if not seams:
        # Not a seam-facing manifest. It must not carry the plane's authority
        # either: naming a Secret it has no use for would mount it into a pod
        # for no reason, and would keep that pod out of Running if the plane
        # is not installed.
        if isinstance(tls, dict) and tls.get("caCertSecretRef") == CA_SECRET:
            problems.append(
                f"{where}: {kind}/{name} names the plane's authority but points at no seam"
            )
        return problems

    for url in seams:
        if not url.startswith("https://"):
            problems.append(
                f"{where}: {kind}/{name} reaches a seam over plaintext: {url}"
            )

    if not isinstance(tls, dict):
        problems.append(
            f"{where}: {kind}/{name} points at a seam over https but names no authority "
            f"(spec.tls.caCertSecretRef: {CA_SECRET}) — it would verify against the system "
            f"trust store, which has never heard of the plane"
        )
        return problems

    # Verification must not be turned off. A seam whose client skips
    # verification costs the same certificate machinery and buys nothing,
    # while looking like it bought something.
    if tls.get("disableVerify"):
        problems.append(
            f"{where}: {kind}/{name} sets disableVerify — a seam nobody verifies is "
            f"weaker than a plaintext one that never claimed to be verified"
        )
    if tls.get("caCertSecretRef") != CA_SECRET:
        problems.append(
            f"{where}: {kind}/{name} verifies against {tls.get('caCertSecretRef')!r}, "
            f"not the plane's authority {CA_SECRET!r}"
        )
    if tls.get("caCertSecretKey") != CA_KEY:
        problems.append(
            f"{where}: {kind}/{name} names key {tls.get('caCertSecretKey')!r}, not {CA_KEY!r}"
        )
    return problems


def scan(root):
    problems, checked = [], 0
    # Every extension a manifest is written in here, not just the one this
    # tree happens to use. scripts/plane-deploy.sh carries a comment about
    # making exactly this mistake: a narrower glob is how a resource gets
    # silently skipped rather than checked.
    for path in sorted(p for pattern in ("*.yaml", "*.yml", "*.json")
                       for p in root.rglob(pattern)):
        try:
            documents = list(yaml.safe_load_all(path.read_text()))
        except yaml.YAMLError as exc:
            problems.append(f"{path}: cannot parse: {exc}")
            continue
        for doc in documents:
            if (isinstance(doc, dict) and doc.get("kind") == "ModelConfig"
                    and any(seam_url(u) for u in urls_in(doc.get("spec") or {}))):
                checked += 1
            problems.extend(check_document(path.relative_to(root), doc))
    return problems, checked


SELF_TEST_CASES = [
    (
        "plaintext seam",
        """
kind: ModelConfig
metadata: {name: bad}
spec:
  tls: {caCertSecretRef: kaimahi-plane-ca, caCertSecretKey: ca.crt}
  openAI: {baseUrl: "http://kaimahi-proxy.kaimahi:8080/upstream/ollama/v1"}
""",
        "plaintext",
    ),
    (
        "https with no authority",
        """
kind: ModelConfig
metadata: {name: bad}
spec:
  openAI: {baseUrl: "https://kaimahi-proxy.kaimahi:8080/upstream/ollama/v1"}
""",
        "names no authority",
    ),
    (
        "verification disabled",
        """
kind: ModelConfig
metadata: {name: bad}
spec:
  openAI: {baseUrl: "https://kaimahi-proxy.kaimahi:8080/upstream/x/v1"}
  tls: {caCertSecretRef: kaimahi-plane-ca, caCertSecretKey: ca.crt, disableVerify: true}
""",
        "disableVerify",
    ),
    (
        "wrong authority",
        """
kind: ModelConfig
metadata: {name: bad}
spec:
  openAI: {baseUrl: "https://kaimahi-proxy.kaimahi:8080/upstream/x/v1"}
  tls: {caCertSecretRef: somebody-elses-ca, caCertSecretKey: ca.crt}
""",
        "not the plane's authority",
    ),
    (
        "wrong key",
        """
kind: ModelConfig
metadata: {name: bad}
spec:
  openAI: {baseUrl: "https://kaimahi-proxy.kaimahi:8080/upstream/x/v1"}
  tls: {caCertSecretRef: kaimahi-plane-ca, caCertSecretKey: tls.crt}
""",
        "names key",
    ),
    (
        "the one-label service name is still a seam",
        """
kind: ModelConfig
metadata: {name: bad}
spec:
  openAI: {baseUrl: "http://kaimahi-proxy:8080/upstream/x/v1"}
""",
        "plaintext",
    ),
    (
        "authority named by a manifest pointing elsewhere",
        """
kind: ModelConfig
metadata: {name: bad}
spec:
  tls: {caCertSecretRef: kaimahi-plane-ca, caCertSecretKey: ca.crt}
  openAI: {baseUrl: "https://api.example.invalid/v1"}
""",
        "points at no seam",
    ),
]

SELF_TEST_CLEAN = """
kind: ModelConfig
metadata: {name: good}
spec:
  tls: {caCertSecretRef: kaimahi-plane-ca, caCertSecretKey: ca.crt}
  openAI: {baseUrl: "https://kaimahi-proxy.kaimahi.svc.cluster.local:8080/upstream/ollama/v1"}
"""

# A manifest with a real URL under a real URL key that is NOT a seam. This is
# what stops a recogniser that answers "seam" to everything from passing: an
# ungoverned ModelConfig must not be required to name the plane's authority,
# because a check that fails on documents it has no business reading is a
# check people turn off.
SELF_TEST_UNRELATED = """
kind: ModelConfig
metadata: {name: direct}
spec:
  openAI: {baseUrl: "https://api.example.invalid/v1"}
"""


def exit_code_failures():
    """Run the real scan over fixture trees and check the exit codes."""
    import contextlib
    import io
    import tempfile

    out = []
    # The fixture trees produce their own verdicts on the way past; only the
    # exit codes matter here, so the noise is swallowed rather than printed
    # between the self-test's own lines.
    hush = io.StringIO()
    with tempfile.TemporaryDirectory() as tmp, \
            contextlib.redirect_stdout(hush), contextlib.redirect_stderr(hush):
        broken, clean = pathlib.Path(tmp) / "broken", pathlib.Path(tmp) / "clean"
        broken.mkdir()
        clean.mkdir()
        for i, (_, document, _) in enumerate(SELF_TEST_CASES):
            (broken / f"{i}.yaml").write_text(document)
        (clean / "a.yaml").write_text(SELF_TEST_CLEAN)
        (clean / "b.yaml").write_text(SELF_TEST_UNRELATED)
        if report(*scan(broken)) != 1:
            out.append("a tree full of broken manifests exited 0")
        if report(*scan(clean)) != 0:
            out.append("a tree of correct manifests exited non-zero")
        # The floor: a scan that read NOTHING must not print the clean
        # verdict. This is the failure that has bitten this repository
        # before — a scanner that examined 0 of 374 files and reported
        # clean — and enumerated is not the same check as examined.
        empty = pathlib.Path(tmp) / "empty"
        empty.mkdir()
        if report(*scan(empty), minimum=MINIMUM_SEAM_MANIFESTS) != 1:
            out.append("an empty tree passed: a scan that read nothing reported everything fine")
        if report(*scan(clean), minimum=MINIMUM_SEAM_MANIFESTS) != 1:
            out.append("a tree below the floor passed")
    return out


def total_cases():
    """Every case the self-test runs: the deliberate breakages, the two
    manifests that must be left alone, and the four exit-code checks."""
    return len(SELF_TEST_CASES) + 9


def self_test():
    """A checker that cannot fail is not a check. Break each rule in turn."""
    failures = []
    for name, document, expected in SELF_TEST_CASES:
        problems = check_document("self-test", yaml.safe_load(document))
        if not any(expected in p for p in problems):
            failures.append(f"{name!r}: expected a complaint containing {expected!r}, got {problems}")
    clean = check_document("self-test", yaml.safe_load(SELF_TEST_CLEAN))
    if clean:
        failures.append(f"a correct manifest was rejected: {clean}")
    # A manifest with a real URL that is not a seam must be left alone. This
    # is the case a recogniser answering "seam" to everything fails.
    unrelated = check_document("self-test", yaml.safe_load(SELF_TEST_UNRELATED))
    if unrelated:
        failures.append(f"an unrelated manifest was flagged: {unrelated}")

    # Raw MCP inventory is not the retired model seam. A third-party tool
    # manifest must neither count toward the floor nor acquire model TLS rules.
    raw_mcp = yaml.safe_load('''
kind: RemoteMCPServer
spec:
  url: http://kaimahi-proxy.kaimahi:8080/raw
''')
    if check_document("raw-mcp", raw_mcp):
        failures.append("raw MCP inventory was treated as a model seam")
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        (root / "raw.yaml").write_text(yaml.safe_dump(raw_mcp))
        if scan(root) != ([], 0):
            failures.append("raw MCP inventory counted toward the model scan floor")
        (root / "direct.yaml").write_text(SELF_TEST_UNRELATED)
        if scan(root) != ([], 0):
            failures.append("direct models counted toward the governed-model floor")

    # The verdict has to reach the exit code, so the real entry point is run
    # over a broken tree and a clean one. A checker that finds every problem,
    # prints it, and exits 0 is a checker nothing is gated on, and that is
    # invisible to a self-test that only inspects the problem list.
    failures.extend(exit_code_failures())
    if failures:
        for f in failures:
            print("  " + f, file=sys.stderr)
        print(f"seam TLS self-test: {len(failures)} of {total_cases()} cases wrong", file=sys.stderr)
        return 1
    print(f"seam TLS self-test: {len(SELF_TEST_CASES)} deliberate breakages all noticed, "
          f"2 correct manifests left alone")
    return 0


# Both committed governed model presets must be examined. Direct model
# presets and raw MCP inventory cannot satisfy this nonvacuous floor.
MINIMUM_SEAM_MANIFESTS = 2


def report(problems, checked, minimum=0):
    """Print the verdict and return the exit code that carries it.

    `minimum` is the floor for a REAL run; the self-test's fixture trees pass
    0 because their size is deliberately independent of the live tree.
    """
    if not problems and checked < minimum:
        print(f"seam TLS: only {checked} seam-capable manifest(s) found, expected at least "
              f"{minimum} — this scan did not read what it is supposed to check",
              file=sys.stderr)
        return 1
    if problems:
        for p in problems:
            print("  " + p, file=sys.stderr)
        print(f"seam TLS: {len(problems)} manifests reach a seam without verifying it", file=sys.stderr)
        return 1
    print(f"seam TLS: {checked} committed seam-capable manifests checked, "
          f"every seam URL is https and names {CA_SECRET}/{CA_KEY}")
    return 0


def main(argv):
    if "--selftest" in argv or "--self-test" in argv:
        return self_test()
    return report(*scan(ROOT / "k8s"), minimum=MINIMUM_SEAM_MANIFESTS)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
