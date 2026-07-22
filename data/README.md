# Replay dataset workflow

TrustMap publishes the canonical December 2025 replay trace at
`data/dune/2025-12/processed/msg.csv`. The processed trace, its manifest, the
Dune query, the acquisition and preparation tools, and the pinned preparation
requirements are tracked in Git. Raw Dune pages under
`data/dune/YYYY-MM/raw/`, raw checksum sidecars, and all `runtime/` environments
and replay outputs are intentionally ignored. Raw pages must remain local and
must not be committed.

The canonical CSV is 27,228,877 bytes, below GitHub's 100 MiB per-file limit,
so it is stored directly in Git and does not require Git LFS.

## Published dataset

The trace comes from Dune query `6515125`, represented by
`data/dune/queries/bridge-flows-2025-12.sql`. The query selects source events in
the half-open UTC interval `[2025-12-01T00:00:00Z,
2026-01-01T00:00:00Z)`. It produced 245,000 prepared rows across 21 chains.
Because the query interval constrains source time, a destination timestamp can
fall after the end of December.

| Property | Published value |
| --- | --- |
| Source time bounds | `2025-12-01T00:00:07Z` to `2025-12-31T23:59:57Z` |
| Destination time bounds | `2025-12-01T00:00:09Z` to `2026-01-14T06:13:08Z` |
| Canonical CSV SHA-256 | `ae35b6fd51185822dcfd8e338b2af43735bb2141764feead9ec993633a232175` |
| Raw-page aggregate SHA-256 | `f8b3f081555d6fe7b6a4a7a376f07e044ef08b07d22375b1cf7e16180dec34ce` |

`data/dune/manifests/bridge-flows-2025-12.json` is the machine-readable
publication record. It fixes the dataset identity and query interval; the
processed trace path, schema, byte digest, row count, chain inventory, and
observed time bounds; and the expected 49-page raw inventory with each page's
name, digest, and row count. `raw_pages.inventory_sha256` hashes the ordered
`<page SHA-256>  <page name>\n` inventory and authenticates that inventory as a
whole.

## Layout

```text
data/
├── README.md
├── requirements.txt
└── dune/
    ├── 2025-12/
    │   ├── processed/msg.csv       # tracked canonical replay input
    │   ├── raw/                    # ignored local acquisition pages
    │   └── raw.sha256              # ignored downloader inventory sidecar
    ├── manifests/bridge-flows-2025-12.json
    ├── queries/bridge-flows-2025-12.sql
    └── scripts/
        ├── download_pages.sh
        ├── prepare_trace.py
        └── verify_dataset.py
```

## Verify the publication

The verifier uses only the Python standard library. Verification of the
tracked publication therefore needs no virtual environment:

```sh
python3 data/dune/scripts/verify_dataset.py \
  --manifest data/dune/manifests/bridge-flows-2025-12.json
```

When the ignored raw archive is present locally, verify its page inventory as
well:

```sh
python3 data/dune/scripts/verify_dataset.py \
  --manifest data/dune/manifests/bridge-flows-2025-12.json \
  --raw-dir data/dune/2025-12/raw
```

The verifier fails closed on schema, CSV structure, hashes, row counts, chain
inventory, time bounds, raw page names, and raw inventory mismatches.

## Reproduce the processed bytes

Trace preparation is pinned to Python 3.13 and the exact package versions in
`data/requirements.txt`. Keep the environment under ignored `runtime/`:

```sh
python3.13 -m venv runtime/replay-data-venv
runtime/replay-data-venv/bin/python -m pip install \
  -r data/requirements.txt
PATH=runtime/replay-data-venv/bin:$PATH \
python3 data/dune/scripts/prepare_trace.py \
  --raw-dir data/dune/2025-12/raw \
  --query-id 6515125 \
  --output runtime/reproduced-msg.csv \
  --expected-rows 245000 \
  --expected-sha256 ae35b6fd51185822dcfd8e338b2af43735bb2141764feead9ec993633a232175
cmp data/dune/2025-12/processed/msg.csv runtime/reproduced-msg.csv
```

The expected row count and digest make the processor prove byte-for-byte
reproduction before `cmp` independently compares the result with the tracked
file.

## Acquire raw pages safely

`download_pages.sh` accepts credentials only through `DUNE_API_KEY`; there is
no command-line key option. Do not place the key in shell history, enable shell
tracing, log it, or store it in the repository. For an interactive download:

```sh
read -r -s DUNE_API_KEY
export DUNE_API_KEY
sh data/dune/scripts/download_pages.sh \
  --query-id 6515125 \
  --output-dir data/dune/2025-12/raw
unset DUNE_API_KEY
```

The downloader sends the key through a mode-`0600` temporary curl header file,
does not follow redirects, accepts only a successful 2xx response, validates
CSV structure, writes pages atomically, and fails rather than replacing an
existing page. `--resume` re-downloads and compares existing pages before it
continues; it does not silently accept changed content. The downloader writes
the ignored inventory to `${output_dir}.sha256`; for the example above that is
`data/dune/2025-12/raw.sha256`. The raw directory and sidecar remain local.

General usage is:

```text
download_pages.sh --query-id ID --output-dir DIR [--limit N] \
  [--start-page N] [--max-pages N] [--resume]
```

For a new query or month, use a new `data/dune/YYYY-MM/raw/` directory and a
new processed output and manifest. Verify and review that new publication
separately. Never use a refresh command to overwrite the published December
2025 canonical trace or its manifest.

## Replay

The full replay uses the tracked canonical trace and its published digest by
default:

```sh
RUN_ROOT=/absolute/path/to/replay-runs ./scripts/replay-full.sh
```

Use `TRACE_PATH` or `--trace` for an intentional custom dataset. A custom trace
does not silently inherit the canonical digest; supply `EXPECTED_DIGEST` or
`--expected-digest` when its bytes should be fixed, and use `--no-golden` when
the canonical full-run golden result is not applicable.
