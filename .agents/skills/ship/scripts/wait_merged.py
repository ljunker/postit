#!/usr/bin/env python3
"""Wait until a GitHub pull request is merged.

Polls `gh pr view <pr> --json state` until the PR leaves the OPEN state.
Standard library only; runs on Linux, macOS and Windows.

Usage:
    python3 wait_merged.py <pr-number-or-url> [--interval 30] [--timeout 21600]

Prints one line per state change and a final line `state=MERGED pr=<n>`.

Exit codes:
    0  merged
    1  closed without merge
    2  usage error or `gh` failed repeatedly
    4  timeout reached while still open
"""
import argparse
import json
import subprocess
import sys
import time


def pr_state(pr: str) -> dict:
    out = subprocess.run(
        ["gh", "pr", "view", pr, "--json", "number,state,mergedAt,url"],
        capture_output=True, text=True, check=True,
    )
    return json.loads(out.stdout)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("pr", help="PR number or URL")
    ap.add_argument("--interval", type=int, default=30, help="seconds between polls (default 30)")
    ap.add_argument("--timeout", type=int, default=6 * 3600, help="give up after this many seconds (default 6h)")
    args = ap.parse_args()

    deadline = time.monotonic() + args.timeout
    failures = 0
    last = None
    while True:
        try:
            info = pr_state(args.pr)
            failures = 0
        except (subprocess.CalledProcessError, json.JSONDecodeError) as exc:
            failures += 1
            print(f"warn=gh_failed attempt={failures} detail={getattr(exc, 'stderr', exc)!s}".strip(), flush=True)
            if failures >= 5:
                return 2
            time.sleep(args.interval)
            continue

        state = info.get("state")
        if state != last:
            print(f"state={state} pr={info.get('number')} url={info.get('url')}", flush=True)
            last = state
        if state == "MERGED":
            return 0
        if state == "CLOSED":
            return 1
        if time.monotonic() >= deadline:
            print(f"state=TIMEOUT pr={info.get('number')}", flush=True)
            return 4
        time.sleep(args.interval)


if __name__ == "__main__":
    sys.exit(main())
