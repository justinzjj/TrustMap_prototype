#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
verifier=$repo_root/data/dune/scripts/verify_dataset.py
fixture_root=$(mktemp -d)
trap 'rm -rf "$fixture_root"' EXIT HUP INT TERM

manifest_dir=$fixture_root/manifest
trace_dir=$fixture_root/2025-12/processed
raw_dir=$fixture_root/2025-12/raw
manifest=$manifest_dir/dataset.json
trace=$trace_dir/msg.csv
mkdir -p "$manifest_dir" "$trace_dir" "$raw_dir"

valid_trace() {
  cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201.0,300e0,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
}

valid_trace >"$trace"
sed -n '1,2p' "$trace" >"$raw_dir/6515125_0000.csv"
{ sed -n '1p' "$trace"; sed -n '3p' "$trace"; } >"$raw_dir/6515125_0001.csv"

write_manifest() {
  python3 - "$manifest" "$trace" "$raw_dir" <<'PY'
import hashlib
import json
import pathlib
import sys

manifest_path = pathlib.Path(sys.argv[1])
trace_path = pathlib.Path(sys.argv[2])
raw_dir = pathlib.Path(sys.argv[3])
pages = []
inventory = bytearray()
for page_path in sorted(raw_dir.glob("*.csv")):
    digest = hashlib.sha256(page_path.read_bytes()).hexdigest()
    rows = len(page_path.read_text(encoding="utf-8").splitlines()) - 1
    pages.append({"name": page_path.name, "sha256": digest, "rows": rows})
    inventory.extend(f"{digest}  {page_path.name}\n".encode())

document = {
    "version": 1,
    "dataset_id": "trustmap-dune-2025-12",
    "query": {
        "provider": "dune",
        "id": 6515125,
        "source_time": {
            "start_inclusive": "2025-12-01T00:00:00Z",
            "end_exclusive": "2026-01-01T00:00:00Z",
        },
    },
    "trace": {
        "path": "../2025-12/processed/msg.csv",
        "sha256": hashlib.sha256(trace_path.read_bytes()).hexdigest(),
        "rows": 2,
        "columns": [
            "src_chain", "dst_chain", "src_block_number", "dst_block_number",
            "src_block_time", "dst_block_time", "bridge_name",
        ],
        "chain_count": 3,
        "chains": ["arbitrum", "ethereum", "optimism"],
        "time_bounds": {
            "src_min": "2025-12-01T00:00:00Z",
            "src_max": "2025-12-31T23:59:59Z",
            "dst_min": "2025-12-01T00:01:00Z",
            "dst_max": "2026-01-01T00:01:00Z",
        },
    },
    "raw_pages": {
        "path": "../2025-12/raw",
        "count": 2,
        "rows": 2,
        "inventory_sha256": hashlib.sha256(inventory).hexdigest(),
        "pages": pages,
    },
}
manifest_path.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
PY
}

expect_failure() {
  label=$1
  shift
  if "$@" >"$fixture_root/failure.out" 2>&1; then
    echo "expected verifier failure: $label" >&2
    exit 1
  fi
}

expect_trace_failure() {
  label=$1
  contents=$2
  printf '%s\n' "$contents" >"$trace"
  write_manifest
  expect_failure "$label" python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"
}

write_manifest
secret='must-not-appear-in-verifier-output'
output=$(DUNE_API_KEY=$secret python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir")
printf '%s\n' "$output" | grep -q 'verified'
if printf '%s\n' "$output" | grep -q "$secret"; then
  echo "verifier leaked credentials" >&2
  exit 1
fi

# Raw pages are optional and are not touched unless --raw-dir is supplied.
mv "$raw_dir" "$raw_dir.offline"
python3 "$verifier" --manifest "$manifest" >/dev/null
mv "$raw_dir.offline" "$raw_dir"

# --trace overrides only the local trace location.
cp "$trace" "$fixture_root/trace-override.csv"
python3 "$verifier" --manifest "$manifest" --trace "$fixture_root/trace-override.csv" >/dev/null

python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["trace"]["sha256"] = "0" * 64
path.write_text(json.dumps(data))
PY
expect_failure "digest mismatch" python3 "$verifier" --manifest "$manifest"

valid_trace >"$trace"
write_manifest
expect_trace_failure "missing column" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time
ethereum,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z
CSV
)"
expect_trace_failure "extra column" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name,unexpected
ethereum,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha,x
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta,x
CSV
)"
expect_trace_failure "malformed row" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha,extra
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "negative height" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,-1,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "fractional height" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,1.5,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "blank chain" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "UTC bounds violation" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-11-30T23:59:59Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"

valid_trace >"$trace"
write_manifest
python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["trace"]["chain_count"] = 4
path.write_text(json.dumps(data))
PY
expect_failure "manifest mismatch" python3 "$verifier" --manifest "$manifest"

write_manifest
python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["raw_pages"]["pages"][1]["name"] = "6515125_0002.csv"
path.write_text(json.dumps(data))
PY
expect_failure "page gap" python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"

write_manifest
python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["raw_pages"]["pages"][0]["sha256"] = "f" * 64
path.write_text(json.dumps(data))
PY
expect_failure "page checksum mismatch" python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"

if ! git -C "$repo_root" check-ignore -q data/dune/2025-12/raw/example.csv; then
  echo "raw Dune page is not ignored" >&2
  exit 1
fi
if git -C "$repo_root" check-ignore -q data/dune/2025-12/processed/msg.csv; then
  echo "canonical processed trace is ignored" >&2
  exit 1
fi

echo "replay dataset verification integration test passed"
