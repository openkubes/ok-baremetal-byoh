#!/usr/bin/env python3
"""Validate JUnit reports for a test tier.

Enforces the M1.3b-2 evidence rules so a green check always means tests really
ran and nothing was silently skipped. Exits non-zero if any of the following:

- the reports directory is missing;
- fewer than --min-reports JUnit files are present;
- any report is empty, unparseable, or contains no <testsuite>;
- an --expect substring matches no report filename (expected per-tier report
  missing);
- the number of executed (non-skipped) tests is below --min-tests
  (an empty run must not pass);
- any suite reports skipped > 0 (no silent skips; pending specs also surface
  here and are additionally failed by `ginkgo --fail-on-pending`).

This only tightens checks; it never weakens an assertion.
"""
import argparse
import glob
import os
import sys
import xml.etree.ElementTree as ET


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("reports_dir")
    ap.add_argument("--min-reports", type=int, default=1,
                    help="minimum number of JUnit report files required")
    ap.add_argument("--min-tests", type=int, default=1,
                    help="minimum number of executed (non-skipped) tests required")
    ap.add_argument("--expect", action="append", default=[], metavar="SUBSTR",
                    help="a report filename substring that must be present; repeatable")
    args = ap.parse_args()

    if not os.path.isdir(args.reports_dir):
        print(f"ERROR: reports directory not found: {args.reports_dir}", file=sys.stderr)
        return 1

    files = sorted(glob.glob(os.path.join(args.reports_dir, "*.xml")))
    if len(files) < args.min_reports:
        print(f"ERROR: expected >= {args.min_reports} JUnit report(s), found "
              f"{len(files)}: {[os.path.basename(f) for f in files]}", file=sys.stderr)
        return 1

    total_tests = total_skipped = total_failures = total_errors = 0
    for f in files:
        if os.path.getsize(f) == 0:
            print(f"ERROR: empty JUnit report: {f}", file=sys.stderr)
            return 1
        try:
            root = ET.parse(f).getroot()
        except ET.ParseError as e:
            print(f"ERROR: unparseable JUnit report {f}: {e}", file=sys.stderr)
            return 1
        suites = list(root.iter("testsuite"))
        if not suites:
            print(f"ERROR: JUnit report has no <testsuite>: {f}", file=sys.stderr)
            return 1
        for ts in suites:
            total_tests += int(ts.get("tests", "0") or 0)
            total_skipped += int(ts.get("skipped", "0") or 0)
            total_failures += int(ts.get("failures", "0") or 0)
            total_errors += int(ts.get("errors", "0") or 0)

    basenames = [os.path.basename(f) for f in files]
    missing = [s for s in args.expect if not any(s in b for b in basenames)]
    if missing:
        print(f"ERROR: expected report(s) not found (no filename matched {missing}); "
              f"have {basenames}", file=sys.stderr)
        return 1

    executed = total_tests - total_skipped
    print(f"reports: {len(files)} {basenames}  tests: {total_tests}  "
          f"executed: {executed}  skipped: {total_skipped}  "
          f"failures: {total_failures}  errors: {total_errors}")

    rc = 0
    if executed < args.min_tests:
        print(f"ERROR: only {executed} executed test(s) (< {args.min_tests}) — an "
              "empty or no-op run must not pass.", file=sys.stderr)
        rc = 1
    if total_skipped > 0:
        print(f"ERROR: {total_skipped} skipped spec(s) — tests must not be silently "
              "skipped (M1.3b-2).", file=sys.stderr)
        rc = 1
    return rc


if __name__ == "__main__":
    sys.exit(main())
