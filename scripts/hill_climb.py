#!/usr/bin/env python3
"""Compare one Go change with an accepted baseline. Keep raw evidence per round."""

import argparse
import json
import math
import re
import statistics
import subprocess
from pathlib import Path

BENCH_ROW = re.compile(r"^Benchmark(\S+?)\s+\d+\s+(.*)$")
UNITS = ("ns/op", "B/op", "allocs/op")


def run(cwd, command, log, timeout=420):
    print(f"[{log.stem}] {' '.join(command)}", flush=True)
    with log.open("w", encoding="utf-8") as output:
        return subprocess.run(
            command, cwd=cwd, stdout=output, stderr=subprocess.STDOUT,
            timeout=timeout, check=False,
        ).returncode


def measurements(log, cases):
    rows = {}
    for line in log.read_text(encoding="utf-8").splitlines():
        match = BENCH_ROW.match(line)
        if not match:
            continue
        name = re.sub(r"-\d+$", "", match[1])
        fields = match[2].split()
        values = {unit: float(value) for value, unit in zip(fields[::2], fields[1::2]) if unit in UNITS}
        if name in rows or values.keys() != set(UNITS) or any(
            not math.isfinite(value) or value < 0 for value in values.values()
        ) or values["ns/op"] == 0:
            raise ValueError(f"invalid or duplicate benchmark result in {log}: {line}")
        rows[name] = values
    if rows.keys() != set(cases):
        raise ValueError(f"expected {cases}, saw {sorted(rows)} in {log}")
    return rows


def compare(samples, args):
    medians = {
        side: {
            case: {unit: statistics.median(row[case][unit] for row in runs) for unit in UNITS}
            for case in args.cases
        }
        for side, runs in samples.items()
    }
    reasons = []
    for case in args.cases:
        base, candidate = (medians[side][case] for side in ("base", "candidate"))
        speedup = 1 - candidate["ns/op"] / base["ns/op"]
        if case == args.primary:
            pairs = zip(samples["base"], samples["candidate"])
            wins = sum(1 - b[case]["ns/op"] / a[case]["ns/op"] >= args.min_gain for a, b in pairs)
            if speedup < args.min_gain or wins <= args.rounds // 2:
                reasons.append(f"{case}: primary latency gain is below threshold or inconsistent ({wins}/{args.rounds} pairs)")
        elif -speedup > args.max_slowdown:
            reasons.append(f"{case}: latency regression exceeds tolerance")
        for unit in UNITS[1:]:
            if candidate[unit] > base[unit]:
                reasons.append(f"{case}: {unit} increased")
    return medians, reasons


def evaluate(args, report):
    roots = {"base": args.base, "candidate": args.candidate}
    for side, root in roots.items():
        report["revisions"][side] = {
            "path": str(root),
            "head": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
            "status": subprocess.check_output(["git", "status", "--porcelain"], cwd=root, text=True).splitlines(),
        }

    def check(side, label, command):
        path = args.out / f"{side}-{label}.log"
        if run(roots[side], command, path) != 0:
            report["reasons"].append(f"{side} {label} failed: {path}")
            return False
        return True

    gates = [
        ("focused", ["go", "test", "-count=1", "-timeout=2m", args.package]),
        ("build", ["go", "build", "./..."]),
        ("vet", ["go", "vet", "./..."]),
        ("full", ["go", "test", "-count=1", "-timeout=5m", "./internal/..."]),
        ("diff", ["git", "diff", "--check", "HEAD"]),
    ]
    for label, command in gates:
        if not check("base", label, command):
            return 2
    for label, command in gates + [
        ("race", ["go", "test", "-race", "-count=1", "-timeout=2m", args.package])
    ]:
        if not check("candidate", label, command):
            return 1

    names = [case.split("/") for case in args.cases]
    top = "|".join(sorted({re.escape("Benchmark" + parts[0]) for parts in names}))
    pattern = f"^({top})$"
    if any(len(parts) > 1 for parts in names):
        sub = "|".join(sorted({re.escape(parts[1]) for parts in names}))
        pattern += f"/^({sub})$"
    command = [
        "go", "test", "-run=^$", f"-bench={pattern}", "-benchmem",
        "-cpu=1", "-count=1", f"-benchtime={args.benchtime}", args.package,
    ]
    for index in range(args.rounds):
        order = tuple(roots) if index % 2 == 0 else ("candidate", "base")
        for side in order:
            label = f"bench-{index + 1:02d}"
            if not check(side, label, command):
                return 2
            path = args.out / f"{side}-{label}.log"
            report["samples"][side].append(measurements(path, args.cases))
    report["medians"], report["reasons"] = compare(report["samples"], args)
    return 1 if report["reasons"] else 0


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--base", type=Path, required=True)
    p.add_argument("--candidate", type=Path, required=True)
    p.add_argument("--out", type=Path, required=True, help="new directory for logs and decision JSON")
    p.add_argument("--package", required=True)
    p.add_argument("--cases", nargs="+", required=True, help="Go benchmark names without the Benchmark prefix")
    p.add_argument("--primary", required=True, help="benchmark to improve")
    p.add_argument("--rounds", type=int, default=5)
    p.add_argument("--benchtime", default="200ms")
    p.add_argument("--min-gain", type=float, default=0.10)
    p.add_argument("--max-slowdown", type=float, default=0.15)
    args = p.parse_args()
    if (
        args.base.resolve() == args.candidate.resolve()
        or not 3 <= args.rounds <= 15
        or not 0 < args.min_gain < 1
        or not 0 <= args.max_slowdown < 1
        or args.primary not in args.cases
        or len(set(args.cases)) != len(args.cases)
        or any(not re.fullmatch(r"[A-Za-z0-9_]+(/[A-Za-z0-9_]+)?", case) for case in args.cases)
        or len({len(case.split("/")) for case in args.cases}) != 1
    ):
        p.error("use distinct worktrees, 3–15 rounds, valid thresholds and unique cases of equal nesting depth")
    args.base, args.candidate, args.out = (path.resolve() for path in (args.base, args.candidate, args.out))
    args.out.mkdir(parents=True, exist_ok=False)
    report = {
        "revisions": {}, "samples": {"base": [], "candidate": []}, "reasons": [],
        "protocol": {
            "package": args.package, "cases": args.cases, "primary": args.primary,
            "rounds": args.rounds, "benchtime": args.benchtime, "cpu": 1,
            "min_gain": args.min_gain, "max_slowdown": args.max_slowdown,
        },
    }
    try:
        code = evaluate(args, report)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        report["reasons"].append(str(error))
        code = 2
    report["decision"] = ("accept", "reject", "inconclusive")[code]
    result = args.out / "decision.json"
    result.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(f"{report['decision']}: {result}", flush=True)
    return code


if __name__ == "__main__":
    raise SystemExit(main())
