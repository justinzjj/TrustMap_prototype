# Replay Data Ownership and Deterministic Dijkstra Performance Design

**Status:** approved direction, written specification

**Scope:** make the replay experiment self-contained in `TrustMap_prototype`,
preserve the read-only status of `TrustMap-ETH`, and remove the internal
Dijkstra allocation bottleneck without changing any legacy replay result.

## 1. Non-negotiable boundaries

- The paper and `TrustMap-ETH` remain read-only. Data is copied, never moved or
  deleted from the reference repository.
- Live `TrustView`, evidence verification, Gateway execution and Geth behavior
  are outside this change.
- Replay costs, checkpoint behavior, Dijkstra cutoff, target-chain pruning,
  strict `<` relaxation and queue ordering `(cost, chain, height)` remain
  unchanged.
- The processed December 2025 trace is source-controlled so a normal clone can
  reproduce the published legacy profile without a Dune credential.
- Raw Dune pages, API credentials, databases and generated replay results never
  enter Git.

## 2. Repository data layout

```text
data/
  README.md
  requirements.txt
  dune/
    queries/
      bridge-flows-2025-12.sql
    scripts/
      download_pages.sh
      prepare_trace.py
      verify_dataset.py
    manifests/
      bridge-flows-2025-12.json
    2025-12/
      raw/                         # local-only, ignored
        6515125_0000.csv
        ...
        6515125_0048.csv
      processed/
        msg.csv                    # tracked canonical 245,000-row trace
```

The canonical processed file is copied from the read-only reference input:

```text
TrustMap-ETH/Dune/output_202512/msg.csv
```

Its required SHA-256 is:

```text
ae35b6fd51185822dcfd8e338b2af43735bb2141764feead9ec993633a232175
```

The 49 raw pages are copied locally from
`TrustMap-ETH/Dune/dune_pages_202512/`. They contain exactly 245,000 data rows
and are ignored by Git. The manifest records page count, each page's SHA-256,
an aggregate SHA-256 over the lexicographically sorted
`<digest><two spaces><basename><newline>` inventory, processed digest, row
count, chain count, source Query ID and UTC time window. Using basenames makes
the aggregate independent of the checkout path.

## 3. Query and acquisition contract

The checked-in SQL is one authoritative query, not the three exploratory
one-day variants currently present in the reference `query.sql`. It selects
the exact canonical output fields from `bridges_evms.flows`: source/destination
chain, bridge name, source/destination height, transaction count, USD volume,
source time and destination time. It rejects null chains, heights and source
time, groups by both timestamps and the message coordinates, and applies the
fixed half-open interval to `deposit_block_time` (the exported
`src_block_time`):

```text
[2025-12-01T00:00:00Z, 2026-01-01T00:00:00Z)
```

The query provenance records Dune Query ID `6515125`. The downloader:

- accepts the API key only through the `DUNE_API_KEY` environment variable and
  has no command-line credential option;
- never contains or prints a credential;
- validates numeric query/page parameters;
- writes deterministic numbered pages beneath an explicit output directory;
- uses retry/fail-on-HTTP-error behavior, never follows redirects and rejects
  every non-2xx response;
- refuses to overwrite a non-empty page unless an explicit resume mode finds
  the same content;
- stops on an empty/header-only page and emits a page checksum inventory.

The query is included for reproducibility even though Dune-hosted query text
may evolve independently. The manifest, raw-page checksums and processed
digest are the facts that identify the published dataset.

## 4. Processing contract

`prepare_trace.py` replaces the output-bearing notebook as the executable data
pipeline. It retains the legacy transformation semantics:

1. discover Query-ID-prefixed page files in deterministic page order;
2. reject missing pages, duplicate page numbers and inconsistent headers;
3. concatenate all rows in page order and preserve row order within each page;
4. trim/lower chain names and trim bridge names;
5. parse source timestamps as UTC;
6. parse scientific/decimal heights with half-even integer rounding;
7. perform a stable ascending sort by exactly `src_block_time`, `src_chain`,
   `src_block_number`, `dst_chain`, `dst_block_number`; rows equal on all five
   keys retain their page/row input order;
8. emit exactly `src_chain,dst_chain,bridge_name,src_block_number,` followed by
   `dst_block_number,tx_count,volume_usd,src_block_time,dst_block_time`, using
   the pandas CSV formatting pinned for the canonical export;
9. atomically publish the output only after row-count and digest validation.

The Python/pandas version used for the legacy-compatible export is pinned in
`data/requirements.txt`. `verify_dataset.py` has no Dune credential dependency
and checks:

- required columns;
- SHA-256;
- 245,000 data rows;
- 21 chains;
- UTC bounds;
- non-empty chain names and valid non-negative integer heights;
- manifest consistency.

The copied canonical file must pass verification immediately. Re-running the
processor over the copied raw pages must reproduce the canonical SHA-256
byte-for-byte before the old-repository default path is removed.

## 5. Replay integration

The full replay entry point defaults to:

```text
data/dune/2025-12/processed/msg.csv
```

`TRACE_PATH` and `--trace` remain supported. Documentation and Docker examples
use repository-owned data paths. The full golden trace digest is unchanged.
No code or script may silently fall back to a sibling repository after
migration.

The raw directory, downloaded page inventories and generated processed files
other than the canonical tracked trace are ignored. The manifest and README
explain how to refresh a dataset under a new dated directory without
overwriting the published December dataset.

## 6. Dijkstra internal-state optimization

### 6.1 Motivation and baseline

The `47a59d2` B2 30,000-event probe is the performance and semantic baseline:

- wall time: `5:43.99`;
- user CPU: `424.91s`;
- peak RSS: `252,832 KiB`;
- decision digest:
  `a9e38c664c4960533d760692f394f4dea1627c3342a9ea65d3600eada6bb5ffc`;
- path digest:
  `ad918ad9802fdea1940801b8b5fe7ef71f88b9fd77c622356fe1e27b63435f61`.

The O(1) edge counter is retained. The remaining dominant cost is rebuilding
`map[ReplayBlockKey]` distance/previous state and a fresh heap for nearly every
B2/B3 event.

### 6.2 Stable node IDs

`ReplayTrustView` assigns an internal monotonically increasing node ID when a
new canonical `ReplayBlockKey` is activated. IDs are never reused. It retains:

- key-to-ID lookup;
- ID-to-key lookup;
- the existing key-based adjacency as the graph authority.

Node IDs are an implementation detail. They are not persisted or exported and
cannot affect path ordering.

### 6.3 Reusable search scratch

Each shortest-path call obtains isolated scratch state containing:

- distance values indexed by node ID;
- distance-generation markers;
- predecessor node IDs and predecessor-generation markers;
- a reusable priority-queue backing slice;
- a non-zero search generation.

Generation markers make old array entries invisible without clearing the whole
array. On generation wrap, the marker arrays are cleared and generation restarts
at one. Scratch objects are pooled; concurrent readers never share an active
scratch object. The existing view read lock freezes the node-ID tables and
adjacency for the duration of a search.

Priority-queue items continue to carry the canonical key. `Less` remains
exactly `(cost, chain, height)`. Neighbour iteration, `nodeOK`, cutoff handling,
strict `candidate < known`, overflow checks and selected predecessor semantics
remain unchanged. Path reconstruction translates predecessor IDs through the
ID-to-key table and reads segment kinds/weights from the existing adjacency.

The first version does not replace key-based adjacency, add A*, add stronger
pruning, reverse the search, or change direct cutoffs.

## 7. Verification gates

### 7.1 Data gates

- canonical file checksum and manifest test;
- processor reproduction from all 49 copied raw pages;
- missing/duplicate page and malformed-column rejection tests;
- downloader shell syntax and fake-server/fake-curl behavior tests;
- repository scan proving no API key, raw page or generated result is tracked.

### 7.2 Search semantic gates

- deterministic fixtures for tie, strict relaxation, cutoff and target pruning;
- a test-only reference implementation of the pre-optimization map-based
  Dijkstra compared against the optimized implementation on deterministic
  mixed graphs;
- exact path node/segment/cost comparison, not only total cost;
- concurrent shortest-path race coverage;
- scratch generation reset/wrap coverage;
- coordinator recovery and smoke golden checks.

### 7.3 Performance and full-run gates

The optimized binary reruns the same first-30,000 B2 probe. Both semantic
digests must match the `47a59d2` baseline exactly. A result mismatch rejects the
optimization regardless of speed.

The target is at least a 2x wall-time improvement on the same host. If the
probe does not materially improve, the full run remains paused and the
implementation is profiled/reviewed again; experimental semantics are never
weakened for speed.

After the probe passes, one new run root executes B0, B1, B2 and B3
sequentially with a fixed binary. The run retains logs, SQLite/WAL, manifests,
CSV exports, summaries and resource measurements. The final full golden check
must pass every row-level decision/path digest and aggregate before the run is
reported complete.

## 8. Git and records

- Data/scripts/performance changes use focused commits on
  `feat/prototype-v1`.
- Local raw pages, probe outputs, full outputs, binaries and experiment logs
  remain ignored under `data/**/raw` or `runtime/`.
- The tracked canonical processed trace, manifest, query, scripts, tests and
  documentation are committed.
- Detailed operational records remain under ignored `98-材料/实验记录/` and
  point to commits and runtime directories.
