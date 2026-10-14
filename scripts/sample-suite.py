#!/usr/bin/env python3
"""Create a single-agent lift fixture with canonical JSON references."""
import hashlib
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
if root.exists():
    raise SystemExit("output already exists")
source = pathlib.Path(__file__).resolve().parents[1] / "internal/kmx/agentsuite/testdata/incident-analyst"

def write(name, value):
    # This fixture uses ASCII keys and integer values: sorted compact JSON is JCS.
    data = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
    target = root / name
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(data)
    target.chmod(0o644)
    return "sha256:" + hashlib.sha256(data).hexdigest()

suite = json.loads((source / "agentsuite.json").read_text())
suite["name"] = "hello-world"
agent = json.loads((source / suite["agents"][0]["path"]).read_text())
agent["id"] = "hello-world"
agent["description"] = "AgentSuite image lift sample"
agent["model"] = {"protocol": "openai-compatible", "capabilities": {
    "api": "openai-chat-completions-v1", "contextTokens": 8192,
    "outputTokens": 1024, "streaming": False, "toolCalling": False}}
instructions = b"Answer greetings briefly. Identify yourself as the AgentSuite sample.\n"
(root / "instructions").mkdir(parents=True)
(root / "instructions/hello-world.md").write_bytes(instructions)
agent["instructions"] = {"path": "instructions/hello-world.md", "digest": "sha256:" + hashlib.sha256(instructions).hexdigest()}
suite["agents"] = [{"id": "hello-world", "path": "agents/hello-world.json", "digest": write("agents/hello-world.json", agent)}]
profile = json.loads((source / suite["buildProfiles"][0]["path"]).read_text())
profile["execution"] = {
    "configuration": "agentkit-v0-mounted-v1",
    "kind": "kubernetes-http-v1", "protocol": "openai-chat-v1", "port": 8080, "healthPath": "/healthz",
    "inputs": [
        {"name": "agent-auth", "environment": "AGENTKIT_AUTH_TOKEN", "secret": True},
        {"name": "listen", "environment": "AGENTKIT_BIND", "secret": False},
    ],
}
suite["buildProfiles"][0]["digest"] = write(suite["buildProfiles"][0]["path"], profile)
composition = json.loads((source / suite["compositions"][0]["path"]).read_text())
composition["agent"] = "hello-world"
suite["compositions"] = [{"agent": "hello-world", "platform": composition["platform"], "path": "compositions/hello-world.json", "digest": write("compositions/hello-world.json", composition)}]
catalog = json.loads((source / suite["toolProviderCatalog"]["path"]).read_text())
suite["toolProviderCatalog"]["digest"] = write(suite["toolProviderCatalog"]["path"], catalog)
write("agentsuite.json", suite)
print(root)
