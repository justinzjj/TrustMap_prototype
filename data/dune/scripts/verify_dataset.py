#!/usr/bin/env python3
"""Verify a TrustMap replay dataset without accessing Dune or credentials."""

from __future__ import annotations

import argparse
import csv
from datetime import datetime, timezone
from decimal import Decimal, InvalidOperation
import hashlib
import json
from pathlib import Path
import re
import sys
from typing import Any


class VerificationError(Exception):
    """A dataset or manifest failed closed verification."""


def require_mapping(value: Any, label: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise VerificationError(f"{label} must be an object")
    return value


def require_list(value: Any, label: str) -> list[Any]:
    if not isinstance(value, list):
        raise VerificationError(f"{label} must be an array")
    return value


def require_string(value: Any, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise VerificationError(f"{label} must be a non-blank string")
    return value


def require_count(value: Any, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise VerificationError(f"{label} must be a non-negative integer")
    return value


def require_digest(value: Any, label: str) -> str:
    digest = require_string(value, label)
    if not re.fullmatch(r"[0-9a-f]{64}", digest):
        raise VerificationError(f"{label} must be a lowercase SHA-256 digest")
    return digest


def parse_utc(value: Any, label: str) -> datetime:
    text = require_string(value, label)
    try:
        parsed = datetime.fromisoformat(text[:-1] + "+00:00" if text.endswith("Z") else text)
    except ValueError as error:
        raise VerificationError(f"{label} is not a valid timestamp") from error
    if parsed.tzinfo is None or parsed.utcoffset() != timezone.utc.utcoffset(parsed):
        raise VerificationError(f"{label} must be UTC")
    return parsed.astimezone(timezone.utc)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    try:
        with path.open("rb") as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(chunk)
    except OSError as error:
        raise VerificationError(f"cannot read {path}: {error}") from error
    return digest.hexdigest()


def resolve_path(manifest_path: Path, configured: Any, override: str | None, label: str) -> Path:
    configured_path = require_string(configured, label)
    if override is not None:
        return Path(override).expanduser().resolve()
    local_path = Path(configured_path).expanduser()
    if not local_path.is_absolute():
        local_path = manifest_path.parent / local_path
    return local_path.resolve()


def validate_height(value: str, label: str) -> None:
    text = value.strip()
    try:
        height = Decimal(text)
    except InvalidOperation as error:
        raise VerificationError(f"{label} is not a non-negative integral height") from error
    if not text or not height.is_finite() or height < 0 or height != height.to_integral_value():
        raise VerificationError(f"{label} is not a non-negative integral height")


def read_trace(path: Path, expected_columns: list[str], query_start: datetime, query_end: datetime) -> dict[str, Any]:
    try:
        source = path.open("r", encoding="utf-8", newline="")
    except OSError as error:
        raise VerificationError(f"cannot read trace {path}: {error}") from error

    row_count = 0
    chains: set[str] = set()
    src_times: list[datetime] = []
    dst_times: list[datetime] = []
    with source:
        reader = csv.reader(source, strict=True)
        try:
            header = next(reader)
        except StopIteration as error:
            raise VerificationError("trace is missing its header") from error
        except csv.Error as error:
            raise VerificationError(f"malformed trace header: {error}") from error
        if header != expected_columns:
            raise VerificationError(f"trace columns do not match manifest: got {header!r}")
        indices = {name: index for index, name in enumerate(header)}
        required = (
            "src_chain",
            "dst_chain",
            "src_block_number",
            "dst_block_number",
            "src_block_time",
            "dst_block_time",
        )
        missing = [name for name in required if name not in indices]
        if missing:
            raise VerificationError(f"trace schema is missing required columns: {', '.join(missing)}")
        try:
            for line_number, row in enumerate(reader, start=2):
                if len(row) != len(header):
                    raise VerificationError(
                        f"malformed trace row {line_number}: expected {len(header)} fields, got {len(row)}"
                    )
                source_chain = row[indices["src_chain"]].strip()
                destination_chain = row[indices["dst_chain"]].strip()
                if not source_chain or not destination_chain:
                    raise VerificationError(f"trace row {line_number} contains a blank chain")
                validate_height(row[indices["src_block_number"]], f"trace row {line_number} source height")
                validate_height(row[indices["dst_block_number"]], f"trace row {line_number} destination height")
                source_time = parse_utc(row[indices["src_block_time"]], f"trace row {line_number} source time")
                destination_time = parse_utc(
                    row[indices["dst_block_time"]], f"trace row {line_number} destination time"
                )
                if source_time < query_start or source_time >= query_end:
                    raise VerificationError(f"trace row {line_number} source time is outside query UTC bounds")
                chains.update((source_chain, destination_chain))
                src_times.append(source_time)
                dst_times.append(destination_time)
                row_count += 1
        except csv.Error as error:
            raise VerificationError(f"malformed trace row: {error}") from error

    if row_count == 0:
        raise VerificationError("trace contains no data rows")
    return {
        "rows": row_count,
        "chains": sorted(chains),
        "time_bounds": {
            "src_min": min(src_times),
            "src_max": max(src_times),
            "dst_min": min(dst_times),
            "dst_max": max(dst_times),
        },
    }


def verify_trace(manifest_path: Path, manifest: dict[str, Any], override: str | None) -> Path:
    query = require_mapping(manifest.get("query"), "query")
    if query.get("provider") != "dune":
        raise VerificationError("query.provider must be 'dune'")
    query_id = query.get("id")
    if isinstance(query_id, bool) or not isinstance(query_id, int) or query_id < 0:
        raise VerificationError("query.id must be a non-negative integer")
    source_time = require_mapping(query.get("source_time"), "query.source_time")
    query_start = parse_utc(source_time.get("start_inclusive"), "query.source_time.start_inclusive")
    query_end = parse_utc(source_time.get("end_exclusive"), "query.source_time.end_exclusive")
    if query_start >= query_end:
        raise VerificationError("query source time interval must be non-empty")

    trace = require_mapping(manifest.get("trace"), "trace")
    trace_path = resolve_path(manifest_path, trace.get("path"), override, "trace.path")
    expected_digest = require_digest(trace.get("sha256"), "trace.sha256")
    actual_digest = sha256_file(trace_path)
    if actual_digest != expected_digest:
        raise VerificationError("trace SHA-256 does not match manifest")

    raw_columns = require_list(trace.get("columns"), "trace.columns")
    if any(not isinstance(column, str) or not column for column in raw_columns):
        raise VerificationError("trace.columns must contain non-blank strings")
    expected_columns = list(raw_columns)
    if len(set(expected_columns)) != len(expected_columns):
        raise VerificationError("trace.columns contains duplicates")
    observed = read_trace(trace_path, expected_columns, query_start, query_end)

    expected_rows = require_count(trace.get("rows"), "trace.rows")
    if observed["rows"] != expected_rows:
        raise VerificationError(f"trace row count does not match manifest: got {observed['rows']}")
    expected_chains_raw = require_list(trace.get("chains"), "trace.chains")
    if any(not isinstance(chain, str) or not chain.strip() for chain in expected_chains_raw):
        raise VerificationError("trace.chains must contain non-blank strings")
    expected_chains = sorted(expected_chains_raw)
    if len(set(expected_chains)) != len(expected_chains):
        raise VerificationError("trace.chains contains duplicates")
    expected_chain_count = require_count(trace.get("chain_count"), "trace.chain_count")
    if expected_chain_count != len(expected_chains) or observed["chains"] != expected_chains:
        raise VerificationError("trace chain inventory does not match manifest")

    expected_bounds_raw = require_mapping(trace.get("time_bounds"), "trace.time_bounds")
    expected_bounds = {
        name: parse_utc(expected_bounds_raw.get(name), f"trace.time_bounds.{name}")
        for name in ("src_min", "src_max", "dst_min", "dst_max")
    }
    if observed["time_bounds"] != expected_bounds:
        raise VerificationError("trace observed UTC bounds do not match manifest")
    return trace_path


def count_page_rows(path: Path) -> int:
    try:
        source = path.open("r", encoding="utf-8", newline="")
    except OSError as error:
        raise VerificationError(f"cannot read raw page {path}: {error}") from error
    with source:
        reader = csv.reader(source, strict=True)
        try:
            header = next(reader)
            if not header:
                raise VerificationError(f"raw page {path.name} has an empty header")
            rows = 0
            for line_number, row in enumerate(reader, start=2):
                if len(row) != len(header):
                    raise VerificationError(f"raw page {path.name} has malformed row {line_number}")
                rows += 1
        except StopIteration as error:
            raise VerificationError(f"raw page {path.name} is empty") from error
        except csv.Error as error:
            raise VerificationError(f"raw page {path.name} is malformed: {error}") from error
    return rows


def verify_raw_pages(manifest_path: Path, manifest: dict[str, Any], override: str) -> Path:
    query = require_mapping(manifest.get("query"), "query")
    query_id = query.get("id")
    if isinstance(query_id, bool) or not isinstance(query_id, int) or query_id < 0:
        raise VerificationError("query.id must be a non-negative integer")
    raw = require_mapping(manifest.get("raw_pages"), "raw_pages")
    raw_dir = resolve_path(manifest_path, raw.get("path"), override, "raw_pages.path")
    count = require_count(raw.get("count"), "raw_pages.count")
    expected_total_rows = require_count(raw.get("rows"), "raw_pages.rows")
    expected_inventory_digest = require_digest(raw.get("inventory_sha256"), "raw_pages.inventory_sha256")
    pages = require_list(raw.get("pages"), "raw_pages.pages")
    if len(pages) != count:
        raise VerificationError("raw page count does not match manifest inventory")

    expected_names = [f"{query_id}_{index:04d}.csv" for index in range(count)]
    manifest_names: list[str] = []
    inventory = bytearray()
    total_rows = 0
    for index, page_value in enumerate(pages):
        page = require_mapping(page_value, f"raw_pages.pages[{index}]")
        name = require_string(page.get("name"), f"raw_pages.pages[{index}].name")
        manifest_names.append(name)
        if name != expected_names[index] or Path(name).name != name:
            raise VerificationError("raw page inventory has a gap or invalid page name")
        expected_digest = require_digest(page.get("sha256"), f"raw_pages.pages[{index}].sha256")
        expected_rows = require_count(page.get("rows"), f"raw_pages.pages[{index}].rows")
        page_path = raw_dir / name
        actual_digest = sha256_file(page_path)
        if actual_digest != expected_digest:
            raise VerificationError(f"raw page checksum does not match manifest: {name}")
        actual_rows = count_page_rows(page_path)
        if actual_rows != expected_rows:
            raise VerificationError(f"raw page row count does not match manifest: {name}")
        total_rows += actual_rows
        inventory.extend(f"{actual_digest}  {name}\n".encode("utf-8"))

    try:
        actual_names = sorted(path.name for path in raw_dir.glob("*.csv") if path.is_file())
    except OSError as error:
        raise VerificationError(f"cannot inventory raw directory {raw_dir}: {error}") from error
    if actual_names != manifest_names:
        raise VerificationError("raw directory page inventory does not match manifest")
    if total_rows != expected_total_rows:
        raise VerificationError("raw page total row count does not match manifest")
    if hashlib.sha256(inventory).hexdigest() != expected_inventory_digest:
        raise VerificationError("raw page aggregate inventory checksum does not match manifest")
    return raw_dir


def load_manifest(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise VerificationError(f"cannot load manifest {path}: {error}") from error
    manifest = require_mapping(value, "manifest")
    if isinstance(manifest.get("version"), bool) or manifest.get("version") != 1:
        raise VerificationError("manifest version must be 1")
    require_string(manifest.get("dataset_id"), "dataset_id")
    return manifest


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", required=True, help="dataset manifest JSON")
    parser.add_argument("--trace", help="override only the local canonical trace path")
    parser.add_argument("--raw-dir", help="verify raw pages in this local directory")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    manifest_path = Path(args.manifest).expanduser().resolve()
    try:
        manifest = load_manifest(manifest_path)
        trace_path = verify_trace(manifest_path, manifest, args.trace)
        raw_path = None
        if args.raw_dir is not None:
            raw_path = verify_raw_pages(manifest_path, manifest, args.raw_dir)
    except VerificationError as error:
        print(f"verification failed: {error}", file=sys.stderr)
        return 1
    suffix = f" and raw pages at {raw_path}" if raw_path is not None else ""
    print(f"verified replay dataset {manifest['dataset_id']!r} at {trace_path}{suffix}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
