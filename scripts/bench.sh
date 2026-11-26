#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
count="${1:-300000}"
stage="$(mktemp -d)"
trap 'rm -rf -- "$stage"' EXIT
mkdir "$stage/spill"
go build -buildvcs=false -o "$stage/ndjson-sort" .
python3 - "$count" > "$stage/input" <<'PY'
import json, random, sys
rng = random.Random(5678)
for i in range(int(sys.argv[1])):
    print(json.dumps({"key": f"{rng.randrange(2048):04d}", "i": i, "data": "p" * 180}, separators=(",", ":")))
PY
wc -c "$stage/input"
python3 - "$stage" <<'PYBENCH'
import pathlib, resource, subprocess, sys, time
stage = pathlib.Path(sys.argv[1])
with (stage / "input").open("rb") as source, (stage / "output").open("wb") as out:
    start = time.monotonic()
    subprocess.run([stage / "ndjson-sort", "-key", "key", "-run-bytes", "2097152",
                    "-max-record", "65536", "-fan-in", "4", "-stats", "-tmpdir",
                    stage / "spill"], stdin=source, stdout=out, check=True)
    elapsed = time.monotonic() - start
units = "bytes" if sys.platform == "darwin" else "KiB" if sys.platform.startswith("linux") else "platform units"
print(f"elapsed={elapsed:.2f} s max_rss={resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss} {units}")
PYBENCH
python3 - "$stage/input" "$stage/output" <<'PY'
import json, sys
with open(sys.argv[1], 'rb') as source:
    expected = sorted(source.readlines(), key=lambda line: json.loads(line)["key"])
with open(sys.argv[2], 'rb') as actual:
    for i, line in enumerate(expected, 1):
        if actual.readline() != line:
            raise SystemExit(f"mismatch against stable reference at output record {i}")
    assert not actual.read(1), "extra output records"
print(f"Verified {len(expected):,} byte-identical records against stable reference")
PY
test -z "$(ls -A "$stage/spill")"
