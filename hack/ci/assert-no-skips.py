#!/usr/bin/env python3
"""Fail if any JUnit report in the given directory records skipped specs.

Enforces the M1.3b-2 rule that no test is silently skipped and counted as
success. Pending specs are already failed by `ginkgo --fail-on-pending`; this
catches runtime `Skip()` that would otherwise pass unnoticed. Exits non-zero if
no reports are found or if any suite reports skipped > 0.
"""
import glob
import os
import sys
import xml.etree.ElementTree as ET


def main(reports_dir: str) -> int:
    files = sorted(glob.glob(os.path.join(reports_dir, "*.xml")))
    if not files:
        print(f"ERROR: no JUnit reports found in {reports_dir}", file=sys.stderr)
        return 1
    total_skipped = 0
    total_tests = 0
    for f in files:
        root = ET.parse(f).getroot()
        for ts in root.iter("testsuite"):
            total_skipped += int(ts.get("skipped", "0") or 0)
            total_tests += int(ts.get("tests", "0") or 0)
    print(f"JUnit files: {len(files)}  tests: {total_tests}  skipped: {total_skipped}")
    if total_skipped > 0:
        print(
            f"ERROR: {total_skipped} skipped spec(s) — tests must not be silently "
            "skipped (M1.3b-2).",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else "_reports"))
