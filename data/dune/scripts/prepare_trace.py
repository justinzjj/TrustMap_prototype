#!/usr/bin/env python3
"""Prepare a deterministic TrustMap replay trace from archived Dune CSV pages."""

from __future__ import annotations

import argparse
import csv
import hashlib
import os
from pathlib import Path
import re
import sys
import tempfile

import pandas as pd


RAW_COLUMNS = [
    "src_chain",
    "dst_chain",
    "bridge_name",
    "src_block_number",
    "dst_block_number",
    "tx_count",
    "volume_usd",
    "src_block_time",
    "dst_block_time",
]
SORT_COLUMNS = [
    "src_block_time",
    "src_chain",
    "src_block_number",
    "dst_chain",
    "dst_block_number",
]


class PreparationError(Exception):
    """The archived pages cannot safely produce a replay trace."""


def positive_digits(value: str) -> str:
    if not re.fullmatch(r"[0-9]+", value) or int(value) <= 0:
        raise argparse.ArgumentTypeError("must contain positive decimal digits")
    return value


def nonnegative_digits(value: str) -> int:
    if not re.fullmatch(r"[0-9]+", value):
        raise argparse.ArgumentTypeError("must contain non-negative decimal digits")
    return int(value)


def sha256_digest(value: str) -> str:
    if not re.fullmatch(r"[0-9a-fA-F]{64}", value):
        raise argparse.ArgumentTypeError("must be a 64-character SHA-256 digest")
    return value.lower()


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="prepare a deterministic replay trace from Dune CSV pages"
    )
    parser.add_argument("--raw-dir", required=True, type=Path)
    parser.add_argument("--query-id", required=True, type=positive_digits)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--expected-rows", type=nonnegative_digits)
    parser.add_argument("--expected-sha256", type=sha256_digest)
    return parser.parse_args()


def discover_pages(raw_dir: Path, query_id: str) -> list[Path]:
    if not raw_dir.is_dir():
        raise PreparationError(f"raw directory does not exist: {raw_dir}")

    canonical = re.compile(rf"{re.escape(query_id)}_([0-9]{{4}})\.csv")
    pages: dict[int, Path] = {}
    for entry in raw_dir.iterdir():
        match = canonical.fullmatch(entry.name)
        if match:
            page_number = int(match.group(1))
            if page_number in pages:
                raise PreparationError(f"duplicate page number {page_number:04d}")
            if not entry.is_file():
                raise PreparationError(f"page is not a regular file: {entry.name}")
            pages[page_number] = entry
        elif entry.name.lower().endswith(".csv"):
            raise PreparationError(f"unexpected Dune page: {entry.name}")

    if not pages:
        raise PreparationError(f"no pages found for query {query_id}")
    expected = list(range(max(pages) + 1))
    if sorted(pages) != expected:
        raise PreparationError("page inventory must be continuous from 0000")
    return [pages[number] for number in expected]


def validate_quote_structure(path: Path) -> None:
    field_start = "field_start"
    unquoted = "unquoted"
    quoted = "quoted"
    after_quote = "after_quote"
    after_quoted_cr = "after_quoted_cr"
    state = field_start
    try:
        source = path.open("r", encoding="utf-8", newline="")
    except OSError as error:
        raise PreparationError(f"cannot read page {path.name}: {error}") from error

    with source:
        while chunk := source.read(1024 * 1024):
            for character in chunk:
                if state == quoted:
                    if character == '"':
                        state = after_quote
                elif state == after_quote:
                    if character == '"':
                        state = quoted
                    elif character == "," or character == "\n":
                        state = field_start
                    elif character == "\r":
                        state = after_quoted_cr
                    else:
                        raise PreparationError(
                            f"page {path.name} has invalid CSV quote structure"
                        )
                elif state == after_quoted_cr:
                    if character != "\n":
                        raise PreparationError(
                            f"page {path.name} has invalid CSV quote structure"
                        )
                    state = field_start
                elif state == field_start:
                    if character == '"':
                        state = quoted
                    elif character == "," or character == "\n":
                        state = field_start
                    else:
                        state = unquoted
                else:
                    if character == '"':
                        raise PreparationError(
                            f"page {path.name} has invalid CSV quote structure"
                        )
                    if character == "," or character == "\n":
                        state = field_start
    if state == quoted or state == after_quoted_cr:
        raise PreparationError(f"page {path.name} has invalid CSV quote structure")


def validate_page(path: Path) -> None:
    validate_quote_structure(path)
    try:
        source = path.open("r", encoding="utf-8", newline="")
    except OSError as error:
        raise PreparationError(f"cannot read page {path.name}: {error}") from error

    with source:
        reader = csv.reader(source, strict=True)
        try:
            header = next(reader)
        except StopIteration as error:
            raise PreparationError(f"page {path.name} is empty") from error
        except csv.Error as error:
            raise PreparationError(f"page {path.name} has malformed CSV: {error}") from error
        if header != RAW_COLUMNS:
            raise PreparationError(f"page {path.name} has an unexpected header")
        row_count = 0
        try:
            for row in reader:
                row_count += 1
                if len(row) != len(RAW_COLUMNS):
                    raise PreparationError(
                        f"page {path.name} has malformed row {row_count + 1}"
                    )
        except csv.Error as error:
            raise PreparationError(f"page {path.name} has malformed CSV: {error}") from error
        if row_count == 0:
            raise PreparationError(f"page {path.name} has no data rows")


def read_pages(paths: list[Path]) -> pd.DataFrame:
    frames: list[pd.DataFrame] = []
    for path in paths:
        validate_page(path)
        try:
            frame = pd.read_csv(path)
        except (OSError, UnicodeError, pd.errors.ParserError) as error:
            raise PreparationError(f"cannot parse page {path.name}: {error}") from error
        if list(frame.columns) != RAW_COLUMNS:
            raise PreparationError(f"page {path.name} has an unexpected header")
        frames.append(frame)
    return pd.concat(frames, ignore_index=True)


def normalize_trace(frame: pd.DataFrame) -> pd.DataFrame:
    for column in ("src_chain", "dst_chain"):
        normalized = frame[column].astype("string").str.strip().str.lower()
        normalized = normalized.mask(normalized == "")
        if normalized.isna().any():
            raise PreparationError(f"{column} contains a missing or blank chain")
        frame[column] = normalized

    frame["bridge_name"] = frame["bridge_name"].astype("string").str.strip()

    for column in ("src_block_number", "dst_block_number"):
        converted = pd.to_numeric(frame[column], errors="coerce").round()
        if converted.isna().any():
            raise PreparationError(f"{column} contains a non-numeric height")
        if (converted < 0).any():
            raise PreparationError(f"{column} contains a negative height")
        try:
            frame[column] = converted.astype("Int64")
        except (OverflowError, TypeError, ValueError) as error:
            raise PreparationError(f"{column} contains an invalid height") from error

    source_time_text = frame["src_block_time"].astype("string").str.removesuffix(" UTC")
    frame["src_block_time"] = pd.to_datetime(
        source_time_text, errors="coerce", utc=True
    )
    if frame["src_block_time"].isna().any():
        raise PreparationError("src_block_time contains an invalid timestamp")

    frame["tx_count"] = (
        pd.to_numeric(frame["tx_count"], errors="coerce").fillna(0).astype(int)
    )
    frame["volume_usd"] = pd.to_numeric(
        frame["volume_usd"], errors="coerce"
    ).fillna(0.0)

    return frame.sort_values(SORT_COLUMNS, kind="stable")[RAW_COLUMNS]


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_atomically(
    frame: pd.DataFrame,
    output: Path,
    expected_rows: int | None,
    expected_sha256: str | None,
) -> tuple[int, str]:
    row_count = len(frame)
    if expected_rows is not None and row_count != expected_rows:
        raise PreparationError(
            f"row count mismatch: expected {expected_rows}, produced {row_count}"
        )
    if not output.parent.is_dir():
        raise PreparationError(f"output directory does not exist: {output.parent}")

    temporary_path: Path | None = None
    try:
        descriptor, name = tempfile.mkstemp(
            dir=output.parent, prefix=f".{output.name}.", suffix=".tmp"
        )
        temporary_path = Path(name)
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as target:
            frame.to_csv(target, index=False)
        digest = file_sha256(temporary_path)
        if expected_sha256 is not None and digest != expected_sha256:
            raise PreparationError(
                f"SHA-256 mismatch: expected {expected_sha256}, produced {digest}"
            )
        os.replace(temporary_path, output)
        temporary_path = None
        return row_count, digest
    finally:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)


def main() -> int:
    arguments = parse_arguments()
    try:
        pages = discover_pages(arguments.raw_dir, arguments.query_id)
        trace = normalize_trace(read_pages(pages))
        rows, digest = write_atomically(
            trace,
            arguments.output,
            arguments.expected_rows,
            arguments.expected_sha256,
        )
    except (PreparationError, OSError) as error:
        print(f"prepare_trace.py: {error}", file=sys.stderr)
        return 1
    print(f"prepared {rows} rows with SHA-256 {digest}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
