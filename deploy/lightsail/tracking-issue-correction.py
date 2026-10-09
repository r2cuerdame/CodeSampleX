"""Rebind only a mistaken merged-PR receipt to a real observation issue."""
import argparse
import json
import re
import subprocess

def issue_number(reference, repository):
    value = reference
    prefix = "https://github.com/" + repository + "/issues/"
    if value.startswith(prefix):
        value = value[len(prefix):]
    if value.startswith("#"):
        value = value[1:]
    if not re.fullmatch(r"[1-9][0-9]*", value):
        raise ValueError("tracking issue must belong to this repository")
    return value

def correct_issue(api, repository, original, corrected, target, event):
    if event != "workflow_dispatch":
        raise ValueError("tracking correction requires a manual observation")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("invalid repository")
    if not re.fullmatch(r"[0-9a-f]{40}", target):
        raise ValueError("invalid authenticated target")
    source = issue_number(original, repository)
    destination = issue_number(corrected, repository)
    pull = api("repos/" + repository + "/pulls/" + source)
    if (pull.get("merged") is not True or pull.get("merge_commit_sha") != target
            or pull.get("base", {}).get("repo", {}).get("full_name") != repository):
        raise ValueError("original tracking PR must be merged as the deployed target")
    issue = api("repos/" + repository + "/issues/" + destination)
    if issue.get("pull_request") is not None or issue.get("number") != int(destination):
        raise ValueError("corrected tracking destination must be a real issue")
    return destination

def github(path):
    result = subprocess.run(["gh", "api", path], check=True, capture_output=True,
                            text=True, timeout=60)
    return json.loads(result.stdout)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("repository", "original", "corrected", "target", "event"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    try:
        print(correct_issue(github, args.repository, args.original,
                            args.corrected, args.target, args.event))
    except (ValueError, subprocess.SubprocessError, TypeError) as exc:
        parser.exit(1, "tracking correction rejected: " + (
            str(exc) if isinstance(exc, ValueError) else "GitHub authentication failed") + "\n")

if __name__ == "__main__":
    main()
