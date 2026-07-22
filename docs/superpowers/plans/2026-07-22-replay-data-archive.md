# Replay Data Archive Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the December 2025 replay dataset, its provenance, acquisition,
processing, and verification self-contained in `TrustMap_prototype` without
modifying the reference repository or tracking raw Dune pages.

**Architecture:** The canonical processed CSV and a checksum manifest are
tracked under `data/dune`; raw pages are copied into an ignored local directory.
Credential-free verification uses the Python standard library, while the
legacy-compatible processor uses a pinned pandas environment. Replay defaults
and documentation point only to the repository-owned canonical trace.

**Tech Stack:** POSIX shell, Python 3, pandas, SQL, JSON, Git, existing Go replay
CLI.

---

## File map

- `data/README.md`: dataset provenance, refresh, verification, and credential
  safety instructions.
- `data/requirements.txt`: pinned pandas dependency for byte-compatible export.
- `data/dune/queries/bridge-flows-2025-12.sql`: fixed December query.
- `data/dune/scripts/download_pages.sh`: paginated Dune downloader.
- `data/dune/scripts/prepare_trace.py`: deterministic legacy processor.
- `data/dune/scripts/verify_dataset.py`: standard-library manifest verifier.
- `data/dune/manifests/bridge-flows-2025-12.json`: immutable published dataset
  metadata and checksums.
- `data/dune/2025-12/processed/msg.csv`: tracked canonical 245,000-row trace.
- `data/dune/2025-12/raw/`: ignored local copy of the 49 source pages.
- `tests/integration/replay_data_test.sh`: data, downloader, processor, and Git
  boundary tests.
- `scripts/replay-full.sh`: repository-owned default trace.
- `.gitignore`, `README.md`, `README.zh-CN.md`, `docs/replay-experiments.md`:
  open-source data workflow and ownership.

### Task 1: Establish data ownership and verification

**Files:**
- Create: `data/dune/scripts/verify_dataset.py`
- Create: `tests/integration/replay_data_test.sh`
- Modify: `.gitignore`

- [ ] **Step 1: Write failing verifier and Git-boundary tests**

Create a temporary CSV/manifest fixture that asserts successful SHA-256, row,
chain, schema, timestamp, and page-inventory validation. Mutate the digest and
assert a non-zero exit. Assert `data/dune/2025-12/raw/example.csv` is ignored
while `data/dune/2025-12/processed/msg.csv` is not ignored.

```sh
sh tests/integration/replay_data_test.sh
```

Expected: FAIL because `verify_dataset.py` and ignore rules do not exist.

- [ ] **Step 2: Implement the standard-library verifier**

Expose this CLI and reject missing/extra columns, malformed rows, invalid
non-negative integral heights, blank chains, digest mismatch, manifest mismatch,
page gaps, page checksum mismatch, and UTC-bound violations:

```text
verify_dataset.py --manifest MANIFEST [--trace CSV] [--raw-dir DIR]
```

The manifest path is the source of expected values; `--trace` and `--raw-dir`
only override local locations. Raw verification is optional unless supplied.

- [ ] **Step 3: Add narrow ignore rules**

Ignore `data/dune/*/raw/**`, page inventories generated beside raw data, and
noncanonical generated processed files. Re-include the dated processed
directory and canonical `msg.csv` explicitly.

- [ ] **Step 4: Run the focused test and commit**

```sh
sh tests/integration/replay_data_test.sh
git check-ignore data/dune/2025-12/raw/example.csv
git check-ignore data/dune/2025-12/processed/msg.csv
```

Expected: test PASS; raw example prints as ignored; canonical trace returns
non-zero from `git check-ignore`.

Commit: `feat: add replay dataset verification boundary`.

### Task 2: Archive query and safe downloader

**Files:**
- Create: `data/dune/queries/bridge-flows-2025-12.sql`
- Create: `data/dune/scripts/download_pages.sh`
- Modify: `tests/integration/replay_data_test.sh`

- [ ] **Step 1: Extend the test with a fake curl executable**

Assert missing credentials fail, numeric arguments are validated, page files
are named `6515125_0000.csv`, header-only termination is not retained, existing
non-empty pages are not overwritten without `--resume`, and the API key never
appears in output. The fake curl writes one data page followed by a header-only
page, so the test uses no network.

Expected before implementation:

```text
FAIL: download_pages.sh not found
```

- [ ] **Step 2: Add the fixed query**

Select and alias the nine canonical columns from `bridges_evms.flows`, filter
`deposit_block_time >= TIMESTAMP '2025-12-01 00:00:00 UTC'` and `< TIMESTAMP
'2026-01-01 00:00:00 UTC'`, reject null chain/height/time fields, group by both
times and message coordinates, and use a deterministic final ordering.

- [ ] **Step 3: Implement the downloader**

Support:

```text
download_pages.sh --query-id ID --output-dir DIR [--limit N]
                  [--start-page N] [--max-pages N] [--resume]
```

Read credentials from `DUNE_API_KEY` only. Download to a temporary file with
`curl --fail --silent --show-error --location --retry 5`, compare before resume
replacement, atomically rename complete pages, and write a basename-only
SHA-256 inventory after completion.

- [ ] **Step 4: Run tests and commit**

```sh
sh -n data/dune/scripts/download_pages.sh
sh tests/integration/replay_data_test.sh
```

Expected: PASS with no real API call.

Commit: `feat: archive reproducible Dune acquisition`.

### Task 3: Reproduce the legacy processing pipeline

**Files:**
- Create: `data/requirements.txt`
- Create: `data/dune/scripts/prepare_trace.py`
- Modify: `tests/integration/replay_data_test.sh`

- [ ] **Step 1: Add synthetic processor tests**

Use two out-of-order pages containing scientific heights, whitespace/case
variants, equal sort keys, and malformed-header/missing-page variants. Assert
the exact output header, normalized strings, half-even heights, stable ordering,
and atomic failure behavior.

Expected before implementation:

```text
FAIL: prepare_trace.py not found
```

- [ ] **Step 2: Pin the compatible processor dependency**

Record the exact pandas version proven to reproduce the checked-in canonical
SHA-256 on Python 3.13. No notebook runtime is required.

- [ ] **Step 3: Implement the processor**

Expose:

```text
prepare_trace.py --raw-dir DIR --query-id ID --output CSV
                 [--expected-rows N] [--expected-sha256 HEX]
```

Require contiguous numbered pages and exact headers. Apply the approved pandas
normalization and stable five-key sort, write a temporary sibling, validate row
count/digest, and replace the output only on success.

- [ ] **Step 4: Run synthetic tests and commit**

```sh
sh tests/integration/replay_data_test.sh
```

Expected: PASS.

Commit: `feat: add deterministic replay trace preparation`.

### Task 4: Copy and identify the published December dataset

**Files:**
- Create locally, ignored: `data/dune/2025-12/raw/6515125_0000.csv` through
  `6515125_0048.csv`
- Create: `data/dune/2025-12/processed/msg.csv`
- Create: `data/dune/manifests/bridge-flows-2025-12.json`

- [ ] **Step 1: Copy, never move, the reference inputs**

Copy from `../TrustMap-ETH/Dune/dune_pages_202512/` and
`../TrustMap-ETH/Dune/output_202512/msg.csv`. Confirm the source files remain
present and unchanged.

- [ ] **Step 2: Build the manifest from local copies**

Record version, Dune Query ID, half-open UTC interval, 49 basename/page digests,
basename-only aggregate digest, canonical trace digest, 245,000 rows, 21 chains,
column list, and observed source/destination bounds.

- [ ] **Step 3: Verify copy and full reproduction**

```sh
python3 data/dune/scripts/verify_dataset.py \
  --manifest data/dune/manifests/bridge-flows-2025-12.json \
  --raw-dir data/dune/2025-12/raw
python3 data/dune/scripts/prepare_trace.py \
  --raw-dir data/dune/2025-12/raw --query-id 6515125 \
  --output runtime/reproduced-msg.csv --expected-rows 245000 \
  --expected-sha256 ae35b6fd51185822dcfd8e338b2af43735bb2141764feead9ec993633a232175
```

Expected: both commands PASS and reproduced SHA-256 equals the canonical hash.

- [ ] **Step 4: Prove Git scope and commit**

```sh
git status --short --ignored data/dune/2025-12
git ls-files data/dune/2025-12/raw
git ls-files data/dune/2025-12/processed/msg.csv
```

Expected: raw directory ignored/untracked; no raw files listed; canonical trace
listed after staging.

Commit: `data: archive canonical December replay trace`.

### Task 5: Switch replay and documentation to repository-owned data

**Files:**
- Create: `data/README.md`
- Modify: `scripts/replay-full.sh`
- Modify: `tests/integration/replay_data_test.sh`
- Modify: `README.md`
- Modify: `README.zh-CN.md`
- Modify: `docs/replay-experiments.md`

- [ ] **Step 1: Add failing default-path and stale-reference assertions**

Assert `replay-full.sh --help` is valid, its default resolves to
`data/dune/2025-12/processed/msg.csv`, and active scripts/docs contain no
`../TrustMap-ETH` fallback or claim that the full trace is absent.

- [ ] **Step 2: Change the full replay default**

Set `default_trace` to the repository-owned canonical CSV while preserving
`TRACE_PATH`, `--trace`, custom digest behavior, and automatic legacy golden
selection.

- [ ] **Step 3: Document acquisition and processing**

Explain tracked versus ignored data, manifest verification, pinned environment
setup, safe Dune credential use, query provenance, raw refresh, byte-for-byte
reproduction, custom trace overrides, and the 26 MiB Git/GitHub rationale in
English and Chinese entry points.

- [ ] **Step 4: Run integration tests and commit**

```sh
sh tests/integration/replay_data_test.sh
sh tests/integration/replay_smoke_test.sh
```

Expected: PASS.

Commit: `docs: make replay data workflow self-contained`.

### Task 6: Final archive gate before experiments

**Files:**
- Modify only if verification reveals a defect in files above.

- [ ] **Step 1: Scan for secrets and forbidden tracked artifacts**

Check tracked files for Dune key assignments and high-entropy credential
patterns without printing matching values. Confirm raw pages, reproduced output,
runtime binaries, databases, and experiment results are not tracked.

- [ ] **Step 2: Run the repository verification suite**

```sh
sh tests/integration/replay_data_test.sh
sh tests/integration/replay_smoke_test.sh
go test ./...
go vet ./...
git diff --check
```

Expected: all PASS.

- [ ] **Step 3: Record the archive checkpoint**

Write the verified canonical/raw digests, commands, dependency version, source
and destination paths, and commits to the ignored experiment record. Confirm
the reference repository and paper repository have no changes caused by this
work.

- [ ] **Step 4: Stop at the stage boundary**

Report the data archive as complete before modifying Dijkstra or starting the
new performance/full replay experiment.
