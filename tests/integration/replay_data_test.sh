#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
verifier=$repo_root/data/dune/scripts/verify_dataset.py
query=$repo_root/data/dune/queries/bridge-flows-2025-12.sql
downloader=$repo_root/data/dune/scripts/download_pages.sh
preparer=$repo_root/data/dune/scripts/prepare_trace.py
requirements=$repo_root/data/requirements.txt
fixture_root=$(mktemp -d)
trap 'rm -rf "$fixture_root"' EXIT HUP INT TERM

missing_acquisition=0
if [ ! -f "$query" ]; then
  echo "missing Dune acquisition query: data/dune/queries/bridge-flows-2025-12.sql" >&2
  missing_acquisition=1
fi
if [ ! -f "$downloader" ]; then
  echo "missing Dune page downloader: data/dune/scripts/download_pages.sh" >&2
  missing_acquisition=1
fi
if [ "$missing_acquisition" -ne 0 ]; then
  exit 1
fi

python3 - "$requirements" <<'PY'
import pathlib
import sys

import dateutil
import numpy
import pandas
import pytz
import six
import tzdata

lines = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()
expected = {
    "numpy==2.3.5",
    "pandas==2.3.3",
    "python-dateutil==2.9.0.post0",
    "pytz==2025.2",
    "six==1.17.0",
    "tzdata==2025.3",
}
if "# Requires Python==3.13.x" not in lines or set(lines) - {"# Requires Python==3.13.x"} != expected:
    raise SystemExit("data requirements are not fully locked")
actual = {
    "numpy": numpy.__version__,
    "pandas": pandas.__version__,
    "python-dateutil": dateutil.__version__,
    "pytz": pytz.__version__,
    "six": six.__version__,
    "tzdata": tzdata.__version__,
}
for requirement in expected:
    name, version = requirement.split("==", 1)
    if actual[name] != version:
        raise SystemExit(f"locked environment mismatch for {name}: {actual[name]}")
PY

manifest_dir=$fixture_root/manifest
trace_dir=$fixture_root/2025-12/processed
raw_dir=$fixture_root/2025-12/raw
manifest=$manifest_dir/dataset.json
trace=$trace_dir/msg.csv
mkdir -p "$manifest_dir" "$trace_dir" "$raw_dir"

prepare_raw=$fixture_root/prepare-raw
prepare_output=$fixture_root/prepared-msg.csv
mkdir -p "$prepare_raw"

prepare_header='src_chain,dst_chain,bridge_name,src_block_number,dst_block_number,tx_count,volume_usd,src_block_time,dst_block_time'

write_prepare_pages() {
  cat >"$prepare_raw/42_0000.csv" <<'CSV'
src_chain,dst_chain,bridge_name,src_block_number,dst_block_number,tx_count,volume_usd,src_block_time,dst_block_time
 Polygon , Ethereum , First ,2.5,4.5,3.9,10.25,2025-12-02 00:00:00.000 UTC,2025-12-02 00:00:10.000 UTC
 ARBITRUM , Base , Early A ,1.25e2,2.5,bad,bad,2025-12-01 00:00:00.000 UTC,2025-12-01 00:00:10.000 UTC
CSV
  cat >"$prepare_raw/42_0001.csv" <<'CSV'
src_chain,dst_chain,bridge_name,src_block_number,dst_block_number,tx_count,volume_usd,src_block_time,dst_block_time
arbitrum,base, Early B ,125.0,2.5,2,1e1,2025-12-01 00:00:00.000 UTC,2025-12-01 00:00:20.000 UTC
 optimism , Arbitrum , Last ,8.5e1,1.005e2,7,0.125,2025-12-03 00:00:00.000 UTC,2025-12-03 00:00:10.000 UTC
CSV
}

expect_prepare_failure() {
  label=$1
  shift
  printf 'existing output\n' >"$prepare_output"
  if "$@" >"$fixture_root/prepare-failure.out" 2>&1; then
    echo "expected trace preparation failure: $label" >&2
    exit 1
  fi
  grep -Fqx 'existing output' "$prepare_output" || {
    echo "trace preparation replaced existing output while rejecting $label" >&2
    exit 1
  }
  if grep -Fq 'Traceback' "$fixture_root/prepare-failure.out"; then
    echo "trace preparation exposed a traceback while rejecting $label" >&2
    cat "$fixture_root/prepare-failure.out" >&2
    exit 1
  fi
  if find "$fixture_root" -maxdepth 1 -name '.prepared-msg.csv.*.tmp' -print | grep -q .; then
    echo "trace preparation left a temporary file while rejecting $label" >&2
    exit 1
  fi
}

write_prepare_pages
python3 "$preparer" --raw-dir "$prepare_raw" --query-id 42 \
  --output "$prepare_output" --expected-rows 4
[ "$(stat -c '%a' "$prepare_output")" = 644 ] || {
  echo "prepared trace mode is not 0644" >&2
  exit 1
}
cat >"$fixture_root/expected-prepared.csv" <<'CSV'
src_chain,dst_chain,bridge_name,src_block_number,dst_block_number,tx_count,volume_usd,src_block_time,dst_block_time
arbitrum,base,Early A,125,2,0,0.0,2025-12-01 00:00:00+00:00,2025-12-01 00:00:10.000 UTC
arbitrum,base,Early B,125,2,2,10.0,2025-12-01 00:00:00+00:00,2025-12-01 00:00:20.000 UTC
polygon,ethereum,First,2,4,3,10.25,2025-12-02 00:00:00+00:00,2025-12-02 00:00:10.000 UTC
optimism,arbitrum,Last,85,100,7,0.125,2025-12-03 00:00:00+00:00,2025-12-03 00:00:10.000 UTC
CSV
cmp "$fixture_root/expected-prepared.csv" "$prepare_output"
prepare_digest=$(sha256sum "$prepare_output" | awk '{print $1}')
python3 "$preparer" --raw-dir "$prepare_raw" --query-id 42 \
  --output "$prepare_output" --expected-rows 4 --expected-sha256 "$prepare_digest"

precision_raw=$fixture_root/precision-raw
precision_output=$fixture_root/precision-msg.csv
mkdir -p "$precision_raw"
cat >"$precision_raw/44_0000.csv" <<'CSV'
src_chain,dst_chain,bridge_name,src_block_number,dst_block_number,tx_count,volume_usd,src_block_time,dst_block_time
ethereum,base,half-even,2.5,1.25e2,1,1,2025-12-01 00:00:00.000 UTC,2025-12-01 00:00:01.000 UTC
CSV
python3 "$preparer" --raw-dir "$precision_raw" --query-id 44 \
  --output "$precision_output" --expected-rows 1
cat >"$fixture_root/expected-precision.csv" <<'CSV'
src_chain,dst_chain,bridge_name,src_block_number,dst_block_number,tx_count,volume_usd,src_block_time,dst_block_time
ethereum,base,half-even,2,125,1,1,2025-12-01 00:00:00+00:00,2025-12-01 00:00:01.000 UTC
CSV
cmp "$fixture_root/expected-precision.csv" "$precision_output"

prepare_output=$precision_output
for unsafe_height in 2.5000000000000001 9.007199254740993e15 18446744073709551615; do
  cat >"$precision_raw/44_0000.csv" <<CSV
$prepare_header
ethereum,base,unsafe,$unsafe_height,1,1,1,2025-12-01 00:00:00.000 UTC,2025-12-01 00:00:01.000 UTC
CSV
  expect_prepare_failure "pandas-incompatible height $unsafe_height" python3 "$preparer" \
    --raw-dir "$precision_raw" --query-id 44 --output "$precision_output"
done
for invalid_height in NaN Inf 1_0 1e99999; do
  cat >"$precision_raw/44_0000.csv" <<CSV
$prepare_header
ethereum,base,invalid,$invalid_height,1,1,1,2025-12-01 00:00:00.000 UTC,2025-12-01 00:00:01.000 UTC
CSV
  expect_prepare_failure "invalid height $invalid_height" python3 "$preparer" \
    --raw-dir "$precision_raw" --query-id 44 --output "$precision_output"
done
prepare_output=$fixture_root/prepared-msg.csv

expect_prepare_failure "zero query id" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 0 --output "$prepare_output"
expect_prepare_failure "non-numeric query id" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 4x --output "$prepare_output"
expect_prepare_failure "row count mismatch" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output" --expected-rows 3
expect_prepare_failure "digest mismatch" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output" \
  --expected-sha256 0000000000000000000000000000000000000000000000000000000000000000

mv "$prepare_raw/42_0001.csv" "$prepare_raw/42_0002.csv"
expect_prepare_failure "missing page" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output"
mv "$prepare_raw/42_0002.csv" "$prepare_raw/42_0001.csv"

cp "$prepare_raw/42_0000.csv" "$prepare_raw/042_0000.csv"
expect_prepare_failure "duplicate noncanonical query page" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output"
unlink "$prepare_raw/042_0000.csv"
cp "$prepare_raw/42_0000.csv" "$prepare_raw/43_0000.csv"
expect_prepare_failure "extra query page" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output"
unlink "$prepare_raw/43_0000.csv"
cp "$prepare_raw/42_0000.csv" "$prepare_raw/42_00.csv"
expect_prepare_failure "extra page-like filename" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output"
unlink "$prepare_raw/42_00.csv"
cp "$prepare_raw/42_0000.csv" "$prepare_raw/42_0000-copy.csv"
expect_prepare_failure "duplicate page copy" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output"
unlink "$prepare_raw/42_0000-copy.csv"

symlink_raw=$fixture_root/symlink-raw
mkdir -p "$symlink_raw"
cp "$prepare_raw/42_0000.csv" "$fixture_root/symlink-target"
ln -s "$fixture_root/symlink-target" "$symlink_raw/45_0000.csv"
expect_prepare_failure "symlink raw page" python3 "$preparer" \
  --raw-dir "$symlink_raw" --query-id 45 --output "$prepare_output"
unlink "$symlink_raw/45_0000.csv"
printf '%s\n' "$prepare_header" >"$symlink_raw/45_0000.csv"
printf '\377\n' >>"$symlink_raw/45_0000.csv"
expect_prepare_failure "invalid UTF-8 raw page" python3 "$preparer" \
  --raw-dir "$symlink_raw" --query-id 45 --output "$prepare_output"

string_raw=$fixture_root/string-raw
string_output=$fixture_root/string-msg.csv
mkdir -p "$string_raw"
cat >"$string_raw/46_0000.csv" <<'CSV'
src_chain,dst_chain,bridge_name,src_block_number,dst_block_number,tx_count,volume_usd,src_block_time,dst_block_time
001,NA,001,1,2,1,1,2025-12-01 00:00:00.000 UTC,2025-12-01 00:00:01.000 UTC
NA,001,NA,3,4,1,1,2025-12-01 00:00:01.000 UTC,2025-12-01 00:00:02.000 UTC
CSV
python3 "$preparer" --raw-dir "$string_raw" --query-id 46 \
  --output "$string_output" --expected-rows 2
cat >"$fixture_root/expected-string.csv" <<'CSV'
src_chain,dst_chain,bridge_name,src_block_number,dst_block_number,tx_count,volume_usd,src_block_time,dst_block_time
001,na,001,1,2,1,1,2025-12-01 00:00:00+00:00,2025-12-01 00:00:01.000 UTC
na,001,NA,3,4,1,1,2025-12-01 00:00:01+00:00,2025-12-01 00:00:02.000 UTC
CSV
cmp "$fixture_root/expected-string.csv" "$string_output"

sed '1s/tx_count/transaction_count/' "$prepare_raw/42_0001.csv" \
  >"$fixture_root/bad-prepare-page"
mv "$fixture_root/bad-prepare-page" "$prepare_raw/42_0001.csv"
expect_prepare_failure "inconsistent header" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output"
write_prepare_pages

cat >"$prepare_raw/42_0001.csv" <<'CSV'
src_chain,dst_chain,bridge_name,src_block_number,dst_block_number,tx_count,volume_usd,src_block_time,dst_block_time
arbitrum,base,"unterminated,125,2,2,10,2025-12-01 00:00:00.000 UTC,2025-12-01 00:00:20.000 UTC
CSV
expect_prepare_failure "malformed CSV" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output"
write_prepare_pages

printf '%s\n' "$prepare_header" >"$prepare_raw/42_0001.csv"
expect_prepare_failure "empty page" python3 "$preparer" \
  --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output"
write_prepare_pages

for invalid_case in blank-src-chain blank-dst-chain bad-src-height negative-src-height bad-dst-height bad-src-time; do
  write_prepare_pages
  case $invalid_case in
    blank-src-chain) sed '3s/^ ARBITRUM /   /' "$prepare_raw/42_0000.csv" >"$fixture_root/invalid-page" ;;
    blank-dst-chain) sed '3s/, Base ,/,   ,/' "$prepare_raw/42_0000.csv" >"$fixture_root/invalid-page" ;;
    bad-src-height) sed '3s/,1.25e2,/,not-a-height,/' "$prepare_raw/42_0000.csv" >"$fixture_root/invalid-page" ;;
    negative-src-height) sed '3s/,1.25e2,/,-1,/' "$prepare_raw/42_0000.csv" >"$fixture_root/invalid-page" ;;
    bad-dst-height) sed '3s/,2.5,bad,/,not-a-height,bad,/' "$prepare_raw/42_0000.csv" >"$fixture_root/invalid-page" ;;
    bad-src-time) sed '3s/2025-12-01 00:00:00.000 UTC/not-a-time/' "$prepare_raw/42_0000.csv" >"$fixture_root/invalid-page" ;;
  esac
  mv "$fixture_root/invalid-page" "$prepare_raw/42_0000.csv"
  expect_prepare_failure "$invalid_case" python3 "$preparer" \
    --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output"
done
write_prepare_pages

for invalid_case in tx-infinity tx-out-of-range volume-infinity volume-nan; do
  write_prepare_pages
  case $invalid_case in
    tx-infinity) sed '3s/,2.5,bad,bad,/,2.5,inf,bad,/' "$prepare_raw/42_0000.csv" >"$fixture_root/invalid-page" ;;
    tx-out-of-range) sed '3s/,2.5,bad,bad,/,2.5,1e999,bad,/' "$prepare_raw/42_0000.csv" >"$fixture_root/invalid-page" ;;
    volume-infinity) sed '3s/,2.5,bad,bad,/,2.5,bad,inf,/' "$prepare_raw/42_0000.csv" >"$fixture_root/invalid-page" ;;
    volume-nan) sed '3s/,2.5,bad,bad,/,2.5,bad,NaN,/' "$prepare_raw/42_0000.csv" >"$fixture_root/invalid-page" ;;
  esac
  mv "$fixture_root/invalid-page" "$prepare_raw/42_0000.csv"
  expect_prepare_failure "$invalid_case" python3 "$preparer" \
    --raw-dir "$prepare_raw" --query-id 42 --output "$prepare_output"
done
write_prepare_pages

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

python3 - "$query" <<'PY'
import pathlib
import re
import sys

sql = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
normalized = re.sub(r"\s+", " ", sql).strip().lower()

select_match = re.search(r"\bselect\s+(.*?)\s+from\s+bridges_evms\.flows\b", normalized)
if not select_match:
    raise SystemExit("Dune query must select from bridges_evms.flows")
expressions = [item.strip() for item in select_match.group(1).split(",")]
expected = [
    "deposit_chain as src_chain",
    "withdrawal_chain as dst_chain",
    "bridge_name as bridge_name",
    "deposit_block_number as src_block_number",
    "withdrawal_block_number as dst_block_number",
    "count(*) as tx_count",
    "sum(amount_usd) as volume_usd",
    "deposit_block_time as src_block_time",
    "withdrawal_block_time as dst_block_time",
]
if expressions != expected:
    raise SystemExit(f"Dune query select list differs: {expressions!r}")

required_fragments = [
    "deposit_chain is not null",
    "withdrawal_chain is not null",
    "deposit_block_number is not null",
    "withdrawal_block_number is not null",
    "deposit_block_time is not null",
    "deposit_block_time >= timestamp '2025-12-01 00:00:00 utc'",
    "deposit_block_time < timestamp '2026-01-01 00:00:00 utc'",
    "group by deposit_chain, withdrawal_chain, bridge_name, deposit_block_number, withdrawal_block_number, deposit_block_time, withdrawal_block_time",
    "order by src_block_time, src_chain, src_block_number, dst_chain, dst_block_number, bridge_name, dst_block_time",
]
for fragment in required_fragments:
    if fragment not in normalized:
        raise SystemExit(f"Dune query missing required clause: {fragment}")
PY

sh -n "$downloader"

fake_bin=$fixture_root/fake-bin
fake_log=$fixture_root/fake-curl.log
mkdir -p "$fake_bin"
cat >"$fake_bin/curl" <<'SH'
#!/bin/sh
set -eu

output=
header=
url=
saw_fail=0
saw_silent=0
saw_show_error=0
retry=
write_out=
expected_key=$(cat "$FAKE_EXPECTED_KEY_FILE")
[ -z "${DUNE_API_KEY+x}" ] || exit 100
while [ "$#" -gt 0 ]; do
  case $1 in
    *"$expected_key"*) exit 101 ;;
  esac
  case $1 in
    --fail) saw_fail=1 ;;
    --silent) saw_silent=1 ;;
    --show-error) saw_show_error=1 ;;
    --location) exit 102 ;;
    --retry)
      [ "$#" -ge 2 ] || exit 91
      retry=$2
      shift
      ;;
    --header)
      [ "$#" -ge 2 ] || exit 92
      header=$2
      shift
      ;;
    --write-out)
      [ "$#" -ge 2 ] || exit 103
      write_out=$2
      shift
      ;;
    --output|-o)
      [ "$#" -ge 2 ] || exit 93
      output=$2
      shift
      ;;
    https://*) url=$1 ;;
    *) exit 94 ;;
  esac
  shift
done

[ "$saw_fail:$saw_silent:$saw_show_error:$retry" = "1:1:1:5" ] || exit 95
[ "$write_out" = '%{http_code}' ] || exit 96
case $header in
  @*) header_file=${header#@} ;;
  *) exit 104 ;;
esac
[ -f "$header_file" ] && [ ! -L "$header_file" ] || exit 105
[ "$(stat -c '%a' "$header_file")" = 600 ] || exit 106
[ "$(cat "$header_file")" = "x-dune-api-key: $expected_key" ] || exit 107
[ -n "$output" ] && [ -n "$url" ] || exit 97
printf '%s\n' "$url" >>"$FAKE_CURL_LOG"

http_code=200
case ${FAKE_CURL_SCENARIO:-} in
  data_then_header)
    case $url in
      *offset=0) printf 'column_a,column_b\nvalue-0,ok\n' >"$output" ;;
      *offset=5000) printf 'column_a,column_b\n' >"$output" ;;
      *) exit 98 ;;
    esac
    ;;
  data_then_empty)
    case $url in
      *offset=0) printf 'column_a,column_b\nvalue-0,ok\n' >"$output" ;;
      *offset=5000) : >"$output" ;;
      *) exit 98 ;;
    esac
    ;;
  data_then_blank)
    case $url in
      *offset=0) printf 'column_a,column_b\nvalue-0,ok\n' >"$output" ;;
      *offset=5000) printf 'column_a,column_b\n\n' >"$output" ;;
      *) exit 98 ;;
    esac
    ;;
  quoted_record)
    printf 'column_a,column_b\n"line one\nline two, ""quoted"" comma",ok\n' >"$output"
    ;;
  malformed_csv)
    printf 'column_a,column_b\n"unterminated,value\n' >"$output"
    ;;
  bare_quote_csv)
    printf 'a,b\n1,bad"quote\n' >"$output"
    ;;
  always_data)
    offset=${url##*offset=}
    printf 'column_a,column_b\nvalue-%s,ok\n' "$offset" >"$output"
    ;;
  resume_sequence)
    case $url in
      *offset=0) printf 'column_a,column_b\nvalue-same,ok\n' >"$output" ;;
      *offset=5000) printf 'column_a,column_b\nvalue-next,ok\n' >"$output" ;;
      *offset=10000) printf 'column_a,column_b\n' >"$output" ;;
      *) exit 98 ;;
    esac
    ;;
  resume_different)
    printf 'column_a,column_b\nvalue-different,ok\n' >"$output"
    ;;
  fail_partial)
    printf 'partial download' >"$output"
    exit 22
    ;;
  redirect)
    printf 'column_a,column_b\nredirected,no\n' >"$output"
    http_code=302
    ;;
  race_target)
    printf 'raced target\n' >"$FAKE_RACE_TARGET"
    printf 'column_a,column_b\ndownloaded,no\n' >"$output"
    ;;
  *) exit 99 ;;
esac
printf '%s' "$http_code"
SH
chmod +x "$fake_bin/curl"

test_key='integration-key-not-for-output'
expected_key_file=$fixture_root/expected-key
printf '%s' "$test_key" >"$expected_key_file"
run_downloader() {
  scenario=$1
  shift
  FAKE_CURL_LOG=$fake_log FAKE_CURL_SCENARIO=$scenario \
    FAKE_EXPECTED_KEY_FILE=$expected_key_file DUNE_API_KEY=$test_key \
    PATH=$fake_bin:$PATH sh "$downloader" "$@"
}

expect_downloader_failure() {
  label=$1
  shift
  if "$@" >"$fixture_root/downloader-failure.out" 2>&1; then
    echo "expected downloader failure: $label" >&2
    exit 1
  fi
  if grep -Fq -- "$test_key" "$fixture_root/downloader-failure.out"; then
    echo "downloader exposed its credential while rejecting $label" >&2
    exit 1
  fi
}

expect_downloader_failure "missing credential" env -u DUNE_API_KEY \
  PATH=$fake_bin:$PATH sh "$downloader" --max-pages 1 --output-dir "$fixture_root/no-key"
expect_downloader_failure "zero query id" run_downloader always_data \
  --query-id 0 --output-dir "$fixture_root/bad-qid"
expect_downloader_failure "non-numeric limit" run_downloader always_data \
  --limit nope --output-dir "$fixture_root/bad-limit"
expect_downloader_failure "negative start page" run_downloader always_data \
  --start-page -1 --output-dir "$fixture_root/bad-start"
expect_downloader_failure "negative max pages" run_downloader always_data \
  --max-pages -1 --output-dir "$fixture_root/bad-max"
expect_downloader_failure "unknown argument" run_downloader always_data \
  --unknown --output-dir "$fixture_root/unknown"
expect_downloader_failure "missing argument value" run_downloader always_data \
  --output-dir "$fixture_root/missing-value" --limit
expect_missing_output_value_failure() {
  (
    cd "$fixture_root"
    run_downloader always_data --output-dir --resume --max-pages 1
  )
}
expect_downloader_failure "option used as missing output value" \
  expect_missing_output_value_failure
[ ! -e "$fixture_root/--resume" ]

if DUNE_API_KEY=$test_key sh "$downloader" --unknown \
  >"$fixture_root/usage.out" 2>&1; then
  echo "expected usage failure" >&2
  exit 1
fi
grep -Fq -- "GNU/Linux" "$fixture_root/usage.out"

empty_path=$fixture_root/empty-path
mkdir -p "$empty_path"
if DUNE_API_KEY=$test_key PATH=$empty_path /bin/sh "$downloader" \
  --max-pages 1 --output-dir "$fixture_root/missing-curl" \
  >"$fixture_root/missing-command.out" 2>&1; then
  echo "expected missing dependency failure" >&2
  exit 1
fi
grep -Fq -- "required command not found: curl" "$fixture_root/missing-command.out"

: >"$fake_log"
bad_key=$(printf 'bad\rkey')
expect_downloader_failure "credential containing CR" env \
  FAKE_CURL_LOG=$fake_log FAKE_CURL_SCENARIO=always_data \
  FAKE_EXPECTED_KEY_FILE=$expected_key_file DUNE_API_KEY="$bad_key" \
  PATH=$fake_bin:$PATH sh "$downloader" --max-pages 1 --output-dir "$fixture_root/bad-key"
[ ! -s "$fake_log" ]

: >"$fake_log"
expect_downloader_failure "start page above four-digit boundary" run_downloader always_data \
  --start-page 10000 --max-pages 1 --output-dir "$fixture_root/start-overflow"
expect_downloader_failure "limit above safe bound" run_downloader always_data \
  --limit 1000001 --max-pages 1 --output-dir "$fixture_root/limit-overflow"
[ ! -s "$fake_log" ]
expect_downloader_failure "max pages crossing page boundary" run_downloader always_data \
  --start-page 9999 --max-pages 2 --output-dir "$fixture_root/page-overflow"
[ "$(wc -l <"$fake_log")" -eq 1 ]

trailing_dir=$fixture_root/trailing
run_downloader always_data --query-id 72 --max-pages 1 \
  --output-dir "$trailing_dir///"
[ -f "$trailing_dir/72_0000.csv" ]
[ -f "$trailing_dir.sha256" ]
[ ! -e "$trailing_dir/.sha256" ]

crlf_dir=$(printf '%s/bad\rpath' "$fixture_root")
expect_downloader_failure "output path containing CR" run_downloader always_data \
  --query-id 73 --max-pages 1 --output-dir "$crlf_dir"
[ ! -e "$crlf_dir" ]

: >"$fake_log"
expect_downloader_failure "filesystem root output" run_downloader always_data \
  --query-id 75 --max-pages 1 --output-dir /
[ ! -s "$fake_log" ]

: >"$fake_log"
download_dir=$fixture_root/download
run_downloader data_then_header --output-dir "$download_dir"
[ -f "$download_dir/6515125_0000.csv" ]
[ ! -e "$download_dir/6515125_0001.csv" ]
[ "$(find "$download_dir" -type f | wc -l)" -eq 1 ]
cat >"$fixture_root/expected-urls" <<'EOF'
https://api.dune.com/api/v1/query/6515125/results/csv?limit=5000&offset=0
https://api.dune.com/api/v1/query/6515125/results/csv?limit=5000&offset=5000
EOF
cmp "$fixture_root/expected-urls" "$fake_log"
if grep -Fq -- "$test_key" "$fake_log"; then
  echo "fake curl log contains downloader credential" >&2
  exit 1
fi
page_digest=$(sha256sum "$download_dir/6515125_0000.csv" | awk '{print $1}')
printf '%s  %s\n' "$page_digest" 6515125_0000.csv >"$fixture_root/expected-inventory"
cmp "$fixture_root/expected-inventory" "$download_dir.sha256"

: >"$fake_log"
redirect_dir=$fixture_root/redirect
expect_downloader_failure "redirect response" run_downloader redirect \
  --query-id 65 --max-pages 1 --output-dir "$redirect_dir"
[ ! -e "$redirect_dir/65_0000.csv" ]
[ ! -e "$redirect_dir.sha256" ]
[ "$(wc -l <"$fake_log")" -eq 1 ]

: >"$fake_log"
empty_dir=$fixture_root/empty-response
run_downloader data_then_empty --query-id 66 --output-dir "$empty_dir"
[ -f "$empty_dir/66_0000.csv" ]
[ ! -e "$empty_dir/66_0001.csv" ]
[ "$(find "$empty_dir" -type f | wc -l)" -eq 1 ]
cat >"$fixture_root/expected-empty-urls" <<'EOF'
https://api.dune.com/api/v1/query/66/results/csv?limit=5000&offset=0
https://api.dune.com/api/v1/query/66/results/csv?limit=5000&offset=5000
EOF
cmp "$fixture_root/expected-empty-urls" "$fake_log"

blank_dir=$fixture_root/blank-response
run_downloader data_then_blank --query-id 67 --output-dir "$blank_dir"
[ -f "$blank_dir/67_0000.csv" ]
[ ! -e "$blank_dir/67_0001.csv" ]

quoted_dir=$fixture_root/quoted-response
run_downloader quoted_record --query-id 68 --max-pages 1 --output-dir "$quoted_dir"
cat >"$fixture_root/expected-quoted.csv" <<'CSV'
column_a,column_b
"line one
line two, ""quoted"" comma",ok
CSV
cmp "$fixture_root/expected-quoted.csv" "$quoted_dir/68_0000.csv"

malformed_dir=$fixture_root/malformed-response
expect_downloader_failure "malformed CSV response" run_downloader malformed_csv \
  --query-id 69 --max-pages 1 --output-dir "$malformed_dir"
[ ! -e "$malformed_dir/69_0000.csv" ]
[ ! -e "$malformed_dir.sha256" ]

bare_quote_dir=$fixture_root/bare-quote-response
expect_downloader_failure "bare quote in CSV response" run_downloader bare_quote_csv \
  --query-id 76 --max-pages 1 --output-dir "$bare_quote_dir"
[ ! -e "$bare_quote_dir/76_0000.csv" ]
[ ! -e "$bare_quote_dir.sha256" ]

race_dir=$fixture_root/race
mkdir -p "$race_dir"
race_target=$race_dir/70_0000.csv
expect_race_failure() {
  FAKE_CURL_LOG=$fake_log FAKE_CURL_SCENARIO=race_target \
    FAKE_EXPECTED_KEY_FILE=$expected_key_file FAKE_RACE_TARGET=$race_target \
    DUNE_API_KEY=$test_key PATH=$fake_bin:$PATH \
    sh "$downloader" --query-id 70 --max-pages 1 --output-dir "$race_dir"
}
expect_downloader_failure "target created during download" expect_race_failure
[ "$(cat "$race_target")" = "raced target" ]
[ ! -e "$race_dir.sha256" ]

symlink_dir=$fixture_root/symlink-existing
mkdir -p "$symlink_dir"
ln -s missing-target "$symlink_dir/71_0000.csv"
expect_downloader_failure "dangling symlink existing page" run_downloader always_data \
  --query-id 71 --max-pages 1 --output-dir "$symlink_dir"
[ -L "$symlink_dir/71_0000.csv" ]

printf 'column_a,column_b\nvalue-0,ok\n' >"$symlink_dir/protected.csv"
ln -s protected.csv "$symlink_dir/74_0000.csv"
: >"$fake_log"
expect_downloader_failure "symlink page with resume" run_downloader always_data \
  --query-id 74 --max-pages 1 --output-dir "$symlink_dir" --resume
[ -L "$symlink_dir/74_0000.csv" ]
[ ! -s "$fake_log" ]

: >"$fake_log"
max_dir=$fixture_root/max-pages
mkdir -p "$max_dir"
printf 'foreign\n' >"$max_dir/foreign.csv"
printf 'other query\n' >"$max_dir/78_0000.csv"
ln -s foreign.csv "$max_dir/77_0002.csv"
run_downloader always_data --query-id 77 --output-dir "$max_dir" \
  --limit 7 --start-page 3 --max-pages 2
[ -f "$max_dir/77_0003.csv" ]
[ -f "$max_dir/77_0004.csv" ]
[ -L "$max_dir/77_0002.csv" ]
cat >"$fixture_root/expected-max-urls" <<'EOF'
https://api.dune.com/api/v1/query/77/results/csv?limit=7&offset=21
https://api.dune.com/api/v1/query/77/results/csv?limit=7&offset=28
EOF
cmp "$fixture_root/expected-max-urls" "$fake_log"
max_digest_0003=$(sha256sum "$max_dir/77_0003.csv" | awk '{print $1}')
max_digest_0004=$(sha256sum "$max_dir/77_0004.csv" | awk '{print $1}')
cat >"$fixture_root/expected-max-inventory" <<EOF
$max_digest_0003  77_0003.csv
$max_digest_0004  77_0004.csv
EOF
cmp "$fixture_root/expected-max-inventory" "$max_dir.sha256"

existing_dir=$fixture_root/existing
mkdir -p "$existing_dir"
printf 'original\n' >"$existing_dir/91_0000.csv"
existing_digest=$(sha256sum "$existing_dir/91_0000.csv" | awk '{print $1}')
expect_downloader_failure "existing page without resume" run_downloader always_data \
  --query-id 91 --output-dir "$existing_dir" --max-pages 1
[ "$(sha256sum "$existing_dir/91_0000.csv" | awk '{print $1}')" = "$existing_digest" ]

zero_existing_dir=$fixture_root/zero-existing
mkdir -p "$zero_existing_dir"
: >"$zero_existing_dir/94_0000.csv"
expect_downloader_failure "zero-byte existing page without resume" run_downloader always_data \
  --query-id 94 --output-dir "$zero_existing_dir" --max-pages 1
[ -e "$zero_existing_dir/94_0000.csv" ]
[ ! -s "$zero_existing_dir/94_0000.csv" ]
expect_downloader_failure "zero-byte resume content mismatch" run_downloader always_data \
  --query-id 94 --output-dir "$zero_existing_dir" --max-pages 1 --resume
[ -e "$zero_existing_dir/94_0000.csv" ]
[ ! -s "$zero_existing_dir/94_0000.csv" ]

resume_dir=$fixture_root/resume
mkdir -p "$resume_dir"
printf 'column_a,column_b\nvalue-same,ok\n' >"$resume_dir/92_0000.csv"
: >"$fake_log"
run_downloader resume_sequence --query-id 92 --output-dir "$resume_dir" --resume
resume_digest=$(sha256sum "$resume_dir/92_0000.csv" | awk '{print $1}')
[ -f "$resume_dir/92_0001.csv" ]
[ ! -e "$resume_dir/92_0002.csv" ]
cat >"$fixture_root/expected-resume-urls" <<'EOF'
https://api.dune.com/api/v1/query/92/results/csv?limit=5000&offset=0
https://api.dune.com/api/v1/query/92/results/csv?limit=5000&offset=5000
https://api.dune.com/api/v1/query/92/results/csv?limit=5000&offset=10000
EOF
cmp "$fixture_root/expected-resume-urls" "$fake_log"
resume_next_digest=$(sha256sum "$resume_dir/92_0001.csv" | awk '{print $1}')
cat >"$fixture_root/expected-resume-inventory" <<EOF
$resume_digest  92_0000.csv
$resume_next_digest  92_0001.csv
EOF
cmp "$fixture_root/expected-resume-inventory" "$resume_dir.sha256"
expect_downloader_failure "resume content mismatch" run_downloader resume_different \
  --query-id 92 --output-dir "$resume_dir" --max-pages 1 --resume
[ "$(sha256sum "$resume_dir/92_0000.csv" | awk '{print $1}')" = "$resume_digest" ]

failure_dir=$fixture_root/curl-failure
mkdir -p "$failure_dir"
printf 'column_a,column_b\nkept,safe\n' >"$failure_dir/93_0000.csv"
failure_digest=$(sha256sum "$failure_dir/93_0000.csv" | awk '{print $1}')
printf 'preexisting inventory\n' >"$failure_dir.sha256"
expect_downloader_failure "curl failure" run_downloader fail_partial \
  --query-id 93 --output-dir "$failure_dir" --max-pages 1 --resume
[ "$(sha256sum "$failure_dir/93_0000.csv" | awk '{print $1}')" = "$failure_digest" ]
[ "$(cat "$failure_dir.sha256")" = "preexisting inventory" ]
[ "$(find "$failure_dir" -type f | wc -l)" -eq 1 ]

echo "replay dataset verification integration test passed"
