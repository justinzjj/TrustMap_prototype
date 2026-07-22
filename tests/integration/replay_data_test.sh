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
ethereum,arbitrum,100,200,2025-12-01T00:00:00+00:00,2025-12-01 00:01:00.000 UTC,alpha
arbitrum,optimism,201.0,300e0,2025-12-31T23:59:59+00:00,2026-01-01 00:01:00.000 UTC,beta
CSV
}

reset_raw_pages() {
  sed -n '1,2p' "$trace" >"$raw_dir/6515125_0000.csv"
  { sed -n '1p' "$trace"; sed -n '3p' "$trace"; } >"$raw_dir/6515125_0001.csv"
}

valid_trace >"$trace"
reset_raw_pages

write_manifest() {
  python3 - "$manifest" "$trace" "$raw_dir" <<'PY'
import hashlib
import csv
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
    with page_path.open(encoding="utf-8", newline="") as page_file:
        rows = sum(1 for _ in csv.reader(page_file, strict=True)) - 1
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
            "dst_min": "2025-12-01 00:01:00.000 UTC",
            "dst_max": "2026-01-01 00:01:00.000 UTC",
        },
    },
    "raw_pages": {
        "path": "../2025-12/raw",
        "count": 2,
        "rows": sum(page["rows"] for page in pages),
        "inventory_sha256": hashlib.sha256(inventory).hexdigest(),
        "pages": pages,
    },
}
manifest_path.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
PY
}

expect_failure() {
  label=$1
  expected=$2
  shift 2
  if "$@" >"$fixture_root/failure.out" 2>&1; then
    echo "expected verifier failure: $label" >&2
    exit 1
  fi
  if ! grep -Fq -- "$expected" "$fixture_root/failure.out"; then
    echo "unexpected verifier error for $label (wanted: $expected)" >&2
    cat "$fixture_root/failure.out" >&2
    exit 1
  fi
}

expect_trace_failure() {
  label=$1
  expected=$2
  contents=$3
  printf '%s\n' "$contents" >"$trace"
  write_manifest
  expect_failure "$label" "$expected" python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"
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

# Match Go encoding/csv for quoted commas, doubled quotes, and multiline fields.
cat >"$trace" <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-12-01T00:00:00+00:00,2025-12-01 00:01:00.000 UTC,"alpha, ""bridge"""
arbitrum,optimism,201.0,300e0,2025-12-31T23:59:59+00:00,2026-01-01 00:01:00.000 UTC,"beta
bridge"
CSV
write_manifest
python3 "$verifier" --manifest "$manifest" >/dev/null

# Preserve the consumer's exact uint64 and legacy half-even height semantics.
cat >"$trace" <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,18446744073709551615,0.5,2025-12-01T00:00:00+00:00,2025-12-01 00:01:00.000 UTC,alpha
arbitrum,optimism,2.5,1.25e2,2025-12-31T23:59:59+00:00,2026-01-01 00:01:00.000 UTC,beta
CSV
write_manifest
python3 "$verifier" --manifest "$manifest" >/dev/null

expect_trace_failure "bare quote in trace" "invalid CSV quote structure" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,bad"quote
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"

python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["trace"]["sha256"] = "0" * 64
path.write_text(json.dumps(data))
PY
expect_failure "digest mismatch" "trace SHA-256 does not match manifest" python3 "$verifier" --manifest "$manifest"

valid_trace >"$trace"
write_manifest
expect_trace_failure "missing column" "trace columns do not match manifest" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time
ethereum,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z
CSV
)"
expect_trace_failure "extra column" "trace columns do not match manifest" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name,unexpected
ethereum,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha,x
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta,x
CSV
)"
expect_trace_failure "malformed row" "malformed trace row" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha,extra
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "negative height" "source height" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,-1,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "underscore height" "non-negative numeric height" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,1_0,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "uint64 overflow height" "exceeds uint64" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,18446744073709551616,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "unbounded exponent" "non-negative numeric height" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,1e1025,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "blank chain" "blank chain" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "UTC bounds violation" "outside query UTC bounds" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-11-30T23:59:59Z,2025-12-01T00:01:00Z,alpha
arbitrum,optimism,201,300,2025-12-31T23:59:59Z,2026-01-01T00:01:00Z,beta
CSV
)"
expect_trace_failure "nonzero UTC offset" "must be UTC" "$(cat <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-12-01T01:00:00+01:00,2025-12-01T00:01:00Z,alpha
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
expect_failure "manifest mismatch" "chain inventory does not match manifest" python3 "$verifier" --manifest "$manifest"

write_manifest
python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["version"] = 1.0
path.write_text(json.dumps(data))
PY
expect_failure "floating manifest version" "manifest version must be exact integer 1" python3 "$verifier" --manifest "$manifest"

write_manifest
python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["raw_pages"]["pages"][1]["name"] = "6515125_0002.csv"
path.write_text(json.dumps(data))
PY
expect_failure "page gap" "raw page inventory has a gap" python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"

write_manifest
python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["raw_pages"]["pages"][0]["sha256"] = "f" * 64
path.write_text(json.dumps(data))
PY
expect_failure "page checksum mismatch" "raw page checksum does not match manifest" python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"

# The raw archive must describe exactly the same rows and schema as the trace.
cp "$trace" "$raw_dir/6515125_0000.csv"
write_manifest
expect_failure "raw and trace row mismatch" "raw page total rows must equal trace rows" \
  python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"
reset_raw_pages

sed -n '1p' "$trace" >"$raw_dir/6515125_0000.csv"
cp "$trace" "$raw_dir/6515125_0001.csv"
write_manifest
expect_failure "empty raw page" "must contain at least one data row" \
  python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"
reset_raw_pages

sed '1s/bridge_name/wrong_bridge_name/' "$raw_dir/6515125_0001.csv" >"$fixture_root/wrong-header.csv"
mv "$fixture_root/wrong-header.csv" "$raw_dir/6515125_0001.csv"
write_manifest
expect_failure "raw header mismatch" "header does not match trace columns" \
  python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"
reset_raw_pages

cat >"$raw_dir/6515125_0000.csv" <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-12-01T00:00:00+00:00,2025-12-01 00:01:00.000 UTC,"alpha, ""bridge
name"""
CSV
write_manifest
python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir" >/dev/null

cat >"$raw_dir/6515125_0000.csv" <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,bad"quote
CSV
write_manifest
expect_failure "bare quote in raw page" "invalid CSV quote structure" \
  python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"

cat >"$raw_dir/6515125_0000.csv" <<'CSV'
src_chain,dst_chain,src_block_number,dst_block_number,src_block_time,dst_block_time,bridge_name
ethereum,arbitrum,100,200,2025-12-01T00:00:00Z,2025-12-01T00:01:00Z,alpha,extra
CSV
write_manifest
expect_failure "malformed raw row" "has malformed row" \
  python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"
reset_raw_pages

write_manifest
python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["raw_pages"]["pages"][0]["rows"] = 2
path.write_text(json.dumps(data))
PY
expect_failure "raw page row manifest mismatch" "raw page row count does not match manifest" \
  python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"

write_manifest
python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["raw_pages"]["rows"] = 3
path.write_text(json.dumps(data))
PY
expect_failure "raw total row manifest mismatch" "raw page total rows must equal trace rows" \
  python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"

write_manifest
python3 - "$manifest" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
data = json.loads(path.read_text())
data["raw_pages"]["inventory_sha256"] = "0" * 64
path.write_text(json.dumps(data))
PY
expect_failure "raw aggregate checksum mismatch" "aggregate inventory checksum" \
  python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"

write_manifest
cp "$raw_dir/6515125_0001.csv" "$raw_dir/6515125_0002.csv"
expect_failure "extra raw inventory" "raw directory page inventory does not match manifest" \
  python3 "$verifier" --manifest "$manifest" --raw-dir "$raw_dir"
unlink "$raw_dir/6515125_0002.csv"

if ! git -C "$repo_root" check-ignore -q data/dune/2025-12/raw/example.csv; then
  echo "raw Dune page is not ignored" >&2
  exit 1
fi
if ! git -C "$repo_root" check-ignore -q data/dune/2025-12/raw.sha256; then
  echo "dated raw inventory sidecar is not ignored" >&2
  exit 1
fi
if git -C "$repo_root" check-ignore -q data/dune/2025-12/processed/msg.csv; then
  echo "canonical processed trace is ignored" >&2
  exit 1
fi
if git -C "$repo_root" check-ignore -q data/dune/scripts/raw_inventory_tool.py; then
  echo "Dune inventory tooling is over-ignored" >&2
  exit 1
fi
if git -C "$repo_root" check-ignore -q data/dune/2025-12/rawness.sha256; then
  echo "unrelated dated sidecar is over-ignored" >&2
  exit 1
fi

echo "replay dataset verification integration test passed"
