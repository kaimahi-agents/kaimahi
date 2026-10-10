#!/usr/bin/env python3
"""CI-only Docker/kind mirror setup and payload-free pull-route evidence."""
from __future__ import annotations

import argparse
import datetime
import json
import os
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path
from urllib.parse import urlsplit

MIRROR = "https://mirror.gcr.io"
HOSTS = '''server = "https://registry-1.docker.io"
[host."https://mirror.gcr.io"]
  capabilities = ["pull", "resolve"]
'''


def merge_daemon_config(config: dict) -> dict:
    return dict(config, **{"registry-mirrors": [MIRROR], "debug": True})


def kind_node_name(args: list[str]) -> str | None:
    if not args or args[0] != "run":
        return None
    name, labels = None, {}
    i = 1
    # kind's run flags use separate values or --flag=value. Stop at the image:
    # labels in an ordinary container's command are not Docker labels.
    switches = {"--detach", "--tty", "--privileged", "--rm", "--init", "--read-only", "-d", "-t", "-i"}
    while i < len(args) and args[i].startswith("-"):
        arg = args[i]
        if arg in switches or "=" in arg:
            i += 1
            continue
        if i + 1 >= len(args):
            return None
        if arg == "--name":
            name = args[i + 1]
        elif arg == "--label":
            key, sep, value = args[i + 1].partition("=")
            if sep:
                labels[key] = value
        i += 2
    if (name and labels.get("io.x-k8s.kind.cluster")
            and labels.get("io.x-k8s.kind.role") in ("control-plane", "worker")):
        return name
    return None


def docker_main(args: list[str], docker_real: str) -> int:
    # Inherited streams keep kind's container ID and kmx JSON output unchanged.
    result = subprocess.run([docker_real, *args], check=False)
    name = kind_node_name(args)
    if result.returncode == 0 and name:
        try:
            setup_node(name, docker_real)
        except (OSError, subprocess.SubprocessError, ValueError) as exc:
            print(f"CI mirror setup failed for kind node {name}: {exc}", file=sys.stderr)
            return 1
    # Python represents a signal exit as -N; shells expose it as 128+N.
    return result.returncode if result.returncode >= 0 else 128 - result.returncode


def state_dir() -> Path:
    return Path(os.environ["CI_MIRROR_STATE"])


def run(args: list[str], **kwargs) -> subprocess.CompletedProcess:
    return subprocess.run(args, check=True, text=True, **kwargs)


def wait_cri(name: str, docker: str) -> None:
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        result = subprocess.run([docker, "exec", name, "crictl", "info"],
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        if result.returncode == 0:
            return
        time.sleep(1)
    raise ValueError("node CRI did not become ready within 30s")


def setup_node(name: str, docker: str) -> None:
    marker = "/etc/containerd/certs.d/docker.io/.kmx-ci-mirror"
    already = subprocess.run([docker, "exec", name, "test", "-f", marker],
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
    if already.returncode != 0:
        # The pinned node's CRI defaults to certs.d. Wait for systemd before
        # restarting only for debug logging; hosts files themselves reload live.
        wait_cri(name, docker)
        run([docker, "exec", "-i", name, "sh", "-c",
             ("mkdir -p /etc/containerd/certs.d/docker.io; "
              "tee /etc/containerd/certs.d/docker.io/hosts.toml >/dev/null")], input=HOSTS)
        config = run([docker, "exec", name, "cat", "/etc/containerd/config.toml"],
                     capture_output=True).stdout
        # CI creates fresh nodes; refuse an unexpected debug table rather than
        # append a duplicate or rewrite an unknown containerd configuration.
        if re.search(r"(?m)^\s*\[debug\]\s*$", config):
            raise ValueError("unexpected existing containerd debug table")
        run([docker, "exec", "-i", name, "sh", "-c",
             "tee -a /etc/containerd/config.toml >/dev/null"],
            input='\n[debug]\nlevel = "debug"\n')
        run([docker, "exec", name, "systemctl", "restart", "containerd"])
        wait_cri(name, docker)
        run([docker, "exec", name, "touch", marker])
    with (state_dir() / "nodes").open("a") as stream:
        stream.write(name + "\n")
    print(f"CI kind mirror: {name}; Docker Hub fallback retained", file=sys.stderr)


def setup(wrap: bool) -> None:
    root = Path(os.environ["RUNNER_TEMP"]) / "registry-mirrors"
    root.mkdir(mode=0o700, exist_ok=True)
    docker = shutil.which("docker")
    if not docker:
        raise ValueError("Docker is unavailable")
    (root / "since").write_text(datetime.datetime.now(datetime.timezone.utc).isoformat())
    (root / "docker-real").write_text(docker)
    # Reload the existing daemon, preserving containers, engine version and
    # unrelated daemon settings. This affects Docker Hub only, not GHCR/ECR.
    run(["sudo", sys.executable, str(Path(__file__).resolve()), "daemon"])
    run(["sudo", "systemctl", "reload", "docker"])
    info = json.loads(run([docker, "info", "--format", "{{json .RegistryConfig.Mirrors}}"],
                          capture_output=True).stdout)
    if MIRROR not in [entry.rstrip("/") for entry in info]:
        raise ValueError("Docker did not reload the registry mirror")
    with Path(os.environ["GITHUB_ENV"]).open("a") as stream:
        stream.write(f"CI_MIRROR_STATE={root}\nCI_MIRROR_DOCKER_REAL={docker}\n")
    if wrap:
        binary_dir = root / "bin"
        binary_dir.mkdir(exist_ok=True)
        helper = binary_dir / "registry-mirrors.py"
        shutil.copy2(__file__, helper)
        helper.chmod(0o755)
        (binary_dir / "docker").symlink_to(helper.name)
        with Path(os.environ["GITHUB_PATH"]).open("a") as stream:
            stream.write(str(binary_dir) + "\n")
    print("CI Docker mirror: mirror.gcr.io; Docker Hub fallback retained")


def setup_cluster(cluster: str, docker: str) -> None:
    names = run([docker, "ps", "--filter", "label=io.x-k8s.kind.cluster=" + cluster,
                 "--format", "{{.Names}}"], capture_output=True).stdout.splitlines()
    found = False
    for name in names:
        role = run([docker, "inspect", "--format",
                    '{{index .Config.Labels "io.x-k8s.kind.role"}}', name],
                   capture_output=True).stdout.strip()
        if role in ("control-plane", "worker"):
            setup_node(name, docker)
            found = True
    if not found:
        raise ValueError("no Kubernetes nodes found in the explicitly selected kind cluster")


def routes(log: str) -> set[tuple[str, str, str]]:
    result = set()
    for line in log.splitlines():
        if 'msg="fetch response received"' not in line or not re.search(
                r'response\.status="?200(?:\s|"|$)', line):
            continue
        match = re.search(r'\burl="?(https://[^\s"\\]+)', line)
        if not match:
            continue
        url = urlsplit(match.group(1))
        if url.hostname not in ("mirror.gcr.io", "registry-1.docker.io"):
            continue
        path = re.fullmatch(r"/v2/(.+)/(manifests|blobs)/[^/]+", url.path)
        if path:
            result.add((url.hostname, path[1], path[2]))
    return result


def print_routes(source: str, log: str) -> None:
    evidence = routes(log)
    for endpoint, image, kind in sorted(evidence):
        route = "mirror" if endpoint == "mirror.gcr.io" else "Docker Hub fallback"
        print(f"{source}: {image} {kind} — {route} ({endpoint}, HTTP 200)")
    if not evidence:
        print(f"{source}: no successful endpoint responses recorded; cache hits are not mirror proof")


def report(docker: str) -> None:
    root = state_dir()
    host = run(["sudo", "journalctl", "-u", "docker", "--since",
                (root / "since").read_text(), "--no-pager"], capture_output=True).stdout
    print_routes("host Docker", host)
    nodes = root / "nodes"
    if nodes.exists():
        for name in sorted(set(nodes.read_text().splitlines())):
            log = run([docker, "exec", name, "journalctl", "-u", "containerd", "--no-pager"],
                      capture_output=True).stdout
            print_routes("kind " + name, log)


def main() -> int:
    if Path(sys.argv[0]).name == "docker":
        return docker_main(sys.argv[1:], os.environ["CI_MIRROR_DOCKER_REAL"])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("setup", "nodes", "report", "daemon"))
    parser.add_argument("--wrap", action="store_true")
    parser.add_argument("--cluster")
    args = parser.parse_args()
    try:
        if args.mode == "daemon":
            config = Path("/etc/docker/daemon.json")
            existing = json.loads(config.read_text()) if config.exists() else {}
            config.parent.mkdir(parents=True, exist_ok=True)
            config.write_text(json.dumps(merge_daemon_config(existing)) + "\n")
        elif args.mode == "setup":
            setup(args.wrap)
        else:
            docker = os.environ["CI_MIRROR_DOCKER_REAL"]
            if args.mode == "nodes":
                if not args.cluster:
                    raise ValueError("--cluster is required")
                setup_cluster(args.cluster, docker)
            else:
                report(docker)
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        print(f"CI registry mirrors: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
