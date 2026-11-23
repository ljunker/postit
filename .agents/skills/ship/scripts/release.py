#!/usr/bin/env python3
"""After a merged PR: clean up the branch, build and push the Docker images, reinstall `p`.

Run it in the MAIN checkout of postit (not in a worktree - the worktree must be
removed before, otherwise its branch cannot be deleted). Standard library only;
runs on Linux, macOS and Windows.

Steps, each printed as one `step=` line:
  1. refuse unless the PR is MERGED
  2. git switch main, git pull --ff-only, git fetch --prune
  3. delete the PR's local branch and, if still present, the remote branch
  4. docker buildx build --platform linux/amd64,linux/arm64 --push for
     kryptikker/postitd (target postitd) and kryptikker/postit-web (target web)
  5. verify with `docker buildx imagetools inspect` that both platforms exist
  6. go install ./cmd/p

Usage:
    python3 release.py --pr <number> [--repo /path/to/postit] [--builder multiarch]
    python3 release.py --no-pr                  # only steps 2, 4-6

Exit codes:
    0  everything done
    2  usage error
    3  PR not merged
    5  a command failed (the failing step is printed)
    6  pushed image lacks a required platform
"""
import argparse
import json
import os
import shutil
import subprocess
import sys

IMAGES = [
    ("kryptikker/postitd:latest", "postitd"),
    ("kryptikker/postit-web:latest", "web"),
]
PLATFORMS = ["linux/amd64", "linux/arm64"]


class StepFailed(Exception):
    pass


def run(cmd, cwd, capture=False, check=True):
    print("$ " + " ".join(cmd), flush=True)
    res = subprocess.run(cmd, cwd=cwd, text=True, capture_output=capture)
    if check and res.returncode != 0:
        if capture:
            sys.stderr.write(res.stdout + res.stderr)
        raise StepFailed(" ".join(cmd))
    return res


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--pr", help="number of the merged PR")
    g.add_argument("--no-pr", action="store_true", help="skip the PR check and branch cleanup")
    ap.add_argument("--repo", default=os.getcwd(), help="main checkout of postit (default: cwd)")
    ap.add_argument("--builder", default="multiarch", help="buildx builder (default multiarch)")
    args = ap.parse_args()
    repo = os.path.abspath(args.repo)

    if os.path.isfile(os.path.join(repo, ".git")):
        print(f"error=is_worktree repo={repo} detail=run this in the main checkout", file=sys.stderr)
        return 2
    for tool in ("git", "docker", "go") + (("gh",) if args.pr else ()):
        if shutil.which(tool) is None:
            print(f"error=missing_tool tool={tool}", file=sys.stderr)
            return 2

    try:
        branch = None
        if args.pr:
            print("step=check_pr", flush=True)
            info = json.loads(run(["gh", "pr", "view", args.pr, "--json", "state,headRefName"],
                                  repo, capture=True).stdout)
            if info["state"] != "MERGED":
                print(f"error=pr_not_merged state={info['state']}", file=sys.stderr)
                return 3
            branch = info["headRefName"]

        print("step=update_main", flush=True)
        run(["git", "switch", "main"], repo)
        run(["git", "pull", "--ff-only"], repo)
        run(["git", "fetch", "--prune"], repo)

        if branch and branch != "main":
            print(f"step=delete_branch branch={branch}", flush=True)
            local = run(["git", "branch", "--list", branch], repo, capture=True).stdout.strip()
            if local:
                run(["git", "branch", "-d", branch], repo)
            remote = run(["git", "ls-remote", "--heads", "origin", branch], repo, capture=True).stdout.strip()
            if remote:
                run(["git", "push", "origin", "--delete", branch], repo)

        for tag, target in IMAGES:
            print(f"step=build_push image={tag}", flush=True)
            run(["docker", "buildx", "build", "--builder", args.builder,
                 "--platform", ",".join(PLATFORMS), "--target", target,
                 "-t", tag, "--push", "."], repo)

        for tag, _ in IMAGES:
            print(f"step=verify image={tag}", flush=True)
            out = run(["docker", "buildx", "imagetools", "inspect", tag], repo, capture=True).stdout
            missing = [p for p in PLATFORMS if p not in out]
            if missing:
                print(f"error=platform_missing image={tag} missing={','.join(missing)}", file=sys.stderr)
                return 6

        print("step=install_p", flush=True)
        run(["go", "install", "./cmd/p"], repo)
        print(f"status=ok p={shutil.which('p') or 'not on PATH'}", flush=True)
        return 0
    except StepFailed as exc:
        print(f"error=command_failed cmd={exc}", file=sys.stderr)
        return 5


if __name__ == "__main__":
    sys.exit(main())
