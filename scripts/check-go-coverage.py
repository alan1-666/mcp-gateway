#!/usr/bin/env python3
"""Fail when declared gateway files are missing or below statement coverage."""
import argparse
import json
from pathlib import Path


def coverage_by_file(profile):
    rows = profile.splitlines()
    if not rows or rows[0] not in {"mode: set", "mode: count", "mode: atomic"}:
        raise ValueError("missing or invalid Go coverage mode")
    blocks = {}
    for line in rows[1:]:
        location, statements, count = line.rsplit(" ", 2)
        filename, position = location.rsplit(":", 1)
        statements, count = int(statements), int(count)
        if statements < 0 or count < 0:
            raise ValueError("negative coverage counter")
        key = (filename, position)
        if key in blocks and blocks[key][0] != statements:
            raise ValueError("conflicting coverage block")
        blocks[key] = (statements, max(count, blocks.get(key, (0, 0))[1]))
    totals = {}
    for (filename, _), (statements, count) in blocks.items():
        covered, total = totals.get(filename, (0, 0))
        totals[filename] = (covered + (statements if count else 0), total + statements)
    return totals


def check(profile, targets):
    totals = coverage_by_file(profile)
    reports, failures = [], []
    for target, minimum in targets.items():
        matches = [(path, value) for path, value in totals.items()
                   if path == target or path.endswith("/" + target)]
        if len(matches) != 1 or matches[0][1][1] == 0:
            failures.append(target + ": missing, ambiguous or empty coverage")
            continue
        covered, total = matches[0][1]
        percent = covered * 100 / total
        reports.append({"file": target, "covered": covered, "statements": total,
                        "percent": round(percent, 2), "minimum": minimum})
        if percent < minimum:
            failures.append(target + ": below required coverage")
    return reports, failures


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    parser.add_argument("--targets", type=Path, default=Path("scripts/go-coverage-targets.json"))
    args = parser.parse_args()
    reports, failures = check(args.profile.read_text(), json.loads(args.targets.read_text()))
    print(json.dumps({"files": reports, "failures": failures}, indent=2))
    return bool(failures)


if __name__ == "__main__":
    raise SystemExit(main())
