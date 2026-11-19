#!/usr/bin/env bash
# Reproducible repo-local Python for BYOH tooling; no global pip installs.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_DIR="$ROOT/.tools/python-venv"
REQUIREMENTS="$ROOT/requirements-tools.txt"
STAMP="$ENV_DIR/.requirements.sha256"
PY="$ENV_DIR/bin/python"
fail() { printf 'STOP: Python toolchain: %s\n' "$*" >&2; exit 1; }
[[ -f "$REQUIREMENTS" && ! -L "$REQUIREMENTS" ]] || fail 'requirements-tools.txt missing or symlink'
command -v python3 >/dev/null 2>&1 || fail 'python3 not found'
command -v shasum >/dev/null 2>&1 || fail 'shasum not found'
mkdir -p "$ROOT/.tools"
[[ ! -L "$ENV_DIR" ]] || fail 'venv path may not be a symlink'
if [[ ! -x "$PY" ]]; then
  [[ ! -e "$ENV_DIR" ]] || fail 'existing venv incomplete; inspect .tools/python-venv before repair'
  python3 -m venv "$ENV_DIR" || fail 'cannot create isolated venv (python3 -m venv)'
fi
digest="$(shasum -a 256 "$REQUIREMENTS" | awk '{print $1}')"
installed=''
[[ ! -L "$STAMP" ]] || fail 'requirements stamp is a symlink'
if [[ -f "$STAMP" ]]; then IFS= read -r installed < "$STAMP" || true; fi
if [[ "$installed" != "$digest" ]] || ! "$PY" -c 'import yaml; assert yaml.__version__ == "6.0.3"' >/dev/null 2>&1; then
  echo 'Preparing pinned BYOH Python dependencies in .tools/python-venv' >&2
  "$PY" -m pip install --disable-pip-version-check --no-input -r "$REQUIREMENTS" \
    || fail 'dependency install failed; check connectivity or configure pip wheelhouse'
  "$PY" -c 'import yaml; assert yaml.__version__ == "6.0.3"' \
    || fail 'installed PyYAML version differs from pin'
  temp="$(mktemp "$ENV_DIR/.requirements.XXXXXXXX")"
  printf '%s\n' "$digest" > "$temp"
  mv -f -- "$temp" "$STAMP"
fi
if [[ "${1:-}" == --check ]]; then
  echo "PASS: isolated Python toolchain (PyYAML 6.0.3)"
  exit 0
fi
[[ "$#" -gt 0 ]] || fail 'specify Python script and arguments or --check'
exec "$PY" "$@"
