#!/usr/bin/env bash
# Fully offline regression tests: never call live kubectl, ssh, or scp.
set -euo pipefail
cd "$(dirname "$0")/../.."
python3 - <<'PY'
from pathlib import Path
import os
import subprocess
import tempfile

script = Path("scripts/scaleway-e2e/byoh-e2e.sh").resolve()

with tempfile.TemporaryDirectory(prefix="ok-byoh-offline-") as tmp:
    root = Path(tmp)
    mockbin = root / "bin"
    mockbin.mkdir()
    remote_dir = root / "remote"
    remote_dir.mkdir()
    kubeconfig = root / "kubeconfig"
    kubeconfig.write_text("MOCK-ONLY\n")
    binary = root / "agent"
    binary.write_text("FAKE AGENT\n")
    log = root / "calls.log"

    def stub(name, source):
        p = mockbin / name
        p.write_text(source)
        p.chmod(0o755)

    stub("kubectl", """#!/usr/bin/env python3
import os, sys
from pathlib import Path
args = ' '.join(sys.argv[1:])
with open(os.environ['MOCK_CALLS'], 'a') as log:
    log.write('kubectl ' + args + '\\n')
if 'config current-context' in args:
    print('kind-ok-byoh-make-test')
elif 'go-template=' in args:
    print('ready')
elif 'bootstrapKubeconfigData' in args:
    if os.environ.get('MOCK_PARTIAL') == '1':
        sys.stdout.write('PARTIAL_CREDENTIAL')
        sys.exit(42)
    sys.stdout.write('MOCK_CREDENTIAL')
else:
    sys.exit(99)
""")

    stub("sha256sum", """#!/usr/bin/env python3
import hashlib, os, sys
from pathlib import Path
p = Path(sys.argv[1])
digest = hashlib.sha256(p.read_bytes()).hexdigest()
if os.environ.get('MOCK_BAD_DIGEST') == '1':
    digest = '0' * 64 if digest != '0' * 64 else '1' * 64
print(digest + '  ' + str(p))
""")

    stub("ssh", """#!/usr/bin/env python3
import os, shlex, subprocess, sys
from pathlib import Path
command = sys.argv[-1]
directory = Path(os.environ['MOCK_REMOTE_DIR'])
target = directory / 'bootstrap.kubeconfig'
with open(os.environ['MOCK_CALLS'], 'a') as log:
    log.write('ssh ' + command.split('\\n', 1)[0] + '\\n')
if 'test ! -e /usr/local/bin/byoh-hostagent' in command:
    # Simulate a host with an existing agent.
    sys.exit(1)
if command.startswith('sudo -n test ! -e /root/.byoh/bootstrap.kubeconfig'):
    sys.exit(1 if target.exists() else 0)
if command.startswith('sudo -n install -d -m 700 /root/.byoh'):
    sys.exit(0)
parts = shlex.split(command)
if parts[:4] != ['sudo', '-n', 'sh', '-c'] or len(parts) != 7:
    sys.stderr.write('Unrecognized mock SSH command\\n')
    sys.exit(98)
remote = parts[4]
remote = remote.replace('dir=/root/.byoh', 'dir=' + shlex.quote(str(directory)), 1)
result = subprocess.run(['sh', '-c', remote, parts[5], parts[6]],
                        input=sys.stdin.buffer.read(), capture_output=True)
sys.stderr.buffer.write(result.stderr)
sys.exit(result.returncode)
""")

    stub("scp", """#!/bin/sh
echo scp >> "$MOCK_CALLS"
exit 99
""")

    envbase = os.environ.copy()
    envbase.update({
        "PATH": str(mockbin) + os.pathsep + envbase["PATH"],
        "MOCK_CALLS": str(log),
        "MOCK_REMOTE_DIR": str(remote_dir),
        "MGMT_KUBECONFIG": str(kubeconfig),
        "AGENT_BINARY": str(binary),
    })

    def run(command, confirmation="", **extra):
        log.write_text("")
        env = envbase.copy()
        env.update(extra)
        return subprocess.run(["bash", str(script), command], input=confirmation,
                              capture_output=True, text=True, timeout=15, env=env)

    confirm = "transfer-bootstrap-ok-bm-lab-02\n"
    result = run("enroll-transfer", confirm, MOCK_PARTIAL="1")
    assert result.returncode != 0, (result.stdout, result.stderr)
    assert "bootstrap credential read failed" in result.stderr, result.stderr
    assert "install -d" not in log.read_text()
    assert not (remote_dir / "bootstrap.kubeconfig").exists()
    print("PASS: partial kubectl stream cannot install credentials")

    result = run("agent-install", "install-agent-ok-bm-lab-02\n")
    assert result.returncode != 0, result.stderr
    assert "agent installation target is not clean" in result.stderr
    assert "scp" not in log.read_text()
    print("PASS: existing agent blocks installation and scp")

    result = run("enroll-transfer", confirm, MOCK_BAD_DIGEST="1")
    assert result.returncode != 0, (result.stdout, result.stderr)
    assert not (remote_dir / "bootstrap.kubeconfig").exists()
    assert not list(remote_dir.glob(".bootstrap.kubeconfig.*"))
    print("PASS: checksum mismatch rejected without temporary leftovers")

    result = run("enroll-transfer", confirm)
    assert result.returncode == 0, (result.stdout, result.stderr)
    target = remote_dir / "bootstrap.kubeconfig"
    assert target.read_bytes() == b"MOCK_CREDENTIAL"
    assert target.stat().st_mode & 0o777 == 0o600
    assert not list(remote_dir.glob(".bootstrap.kubeconfig.*"))
    print("PASS: credential checksum, install, mode 0600 and cleanup")

    result = run("enroll-transfer", confirm)
    assert result.returncode != 0
    assert target.read_bytes() == b"MOCK_CREDENTIAL"
    print("PASS: existing credential never overwritten")

print("PASS: offline BYOH E2E security regression")
PY
