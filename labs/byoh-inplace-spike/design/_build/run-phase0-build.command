#!/bin/bash
# Phase-0 build + unit tests on the Mac's Go 1.26 toolchain.
# Double-click in Finder to run. Writes logs next to this file.
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../../.." && pwd)"   # -> ok-baremetal-byoh
LOG="$HERE"
: > "$LOG/_status.txt"
echo "started $(date)" >> "$LOG/_status.txt"

# Locate go across common macOS locations + login PATH.
# Prefer a go that is already 1.26 (the default homebrew formula) over go@1.24,
# so the build matches the interactive toolchain and skips the auto-download.
for p in /opt/homebrew/bin/go /usr/local/go/bin/go "$HOME/go/bin/go" "$(command -v go 2>/dev/null)" /opt/homebrew/opt/go@1.24/bin/go; do
  if [ -n "$p" ] && [ -x "$p" ]; then
    if "$p" version 2>/dev/null | grep -q "go1.2[6-9]"; then GO="$p"; break; fi
    [ -z "$GO" ] && GO="$p"   # remember a fallback; GOTOOLCHAIN=auto upgrades it to 1.26
  fi
done
if [ -z "$GO" ]; then
  echo "GO_NOT_FOUND" >> "$LOG/_status.txt"
  echo "could not locate the go toolchain" > "$LOG/build.out"
  echo "DONE rc_build=127 rc_test=127" >> "$LOG/_status.txt"
  exit 127
fi
echo "go=$GO" >> "$LOG/_status.txt"
cd "$REPO" || { echo "REPO_NOT_FOUND $REPO" >> "$LOG/_status.txt"; exit 1; }
echo "repo=$REPO" >> "$LOG/_status.txt"
"$GO" version > "$LOG/go-version.txt" 2>&1

echo "== go build ./... ==" > "$LOG/build.out"
"$GO" build ./... >> "$LOG/build.out" 2>&1
RC_BUILD=$?

echo "== go test ./internal/safety/noreplacement/... ./internal/inplace/... -v ==" > "$LOG/test.out"
"$GO" test ./internal/safety/noreplacement/... ./internal/inplace/... -v >> "$LOG/test.out" 2>&1
RC_TEST=$?

echo "DONE rc_build=$RC_BUILD rc_test=$RC_TEST" >> "$LOG/_status.txt"
echo "finished $(date)" >> "$LOG/_status.txt"
