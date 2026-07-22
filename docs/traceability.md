# TrustMap prototype traceability

This file maps the experiment-first Phase 4 acceptance boundary to executable
evidence. The normative design remains in `docs/superpowers/specs/`.

| Requirement | Implementation boundary | Executable evidence |
| --- | --- | --- |
| Confirmed Gateway input and fail-closed canonical progress | `Mapnode/indexer`, `Mapnode/reorg`, append-only store migrations | indexer/reorg/store Go tests; live scripts wait for configured confirmations |
| Real Direct calldata and ordered signatures | `Mapnode/chainabi`, `Mapnode/executor` | ABI golden tests, executor signature tests, `live_two_chain_test.sh` |
| Immutable snapshot-bound Path proof in Solidity order | `Mapnode/proof`, `Mapnode/executor` | proof order/base-root tests, Path calldata tests, `live_three_chain_test.sh` |
| Persist signed transaction before broadcast and recover exact bytes | transaction submission migration/repository/executor | repository and submitter restart/response-loss tests |
| Confirm only after canonical receipt, `requestResolved`, and durable indexer bundle | transaction submitter and confirmed indexer | focused executor tests plus both live receipt/event assertions |
| Verified dependency propagation creates TrustView edges | `Mapnode/p2p`, remote evidence validation, `Mapnode/store` | P2P/validator tests and exact edge assertions in both live scripts |
| Planner selects a real two-hop route without Direct fallback | Planner/coordinator/proof builder | Phase 3 checkpoint plus live Path API/event `hop_count == 2` assertions |
| Static topology range is 2–21 chains | topology config/renderer | example tests and Compose config checks for 2, 3, and 21 chains |
| Legacy-compatible B0--B3 replay without weakening live TrustView | `Mapnode/replay`, replay-only SQLite/WAL and `ReplayTrustView` | replay unit tests and `replay_smoke_test.sh` golden check |
| Full 21-chain cost replay is reproducible and separately calibrated | replay profiles, checkpoint policies, full-run script and aggregate checker | exact `legacy-v4.1` selection, aggregate, graph and hash checks |

## Live acceptance commands

```sh
./tests/integration/live_two_chain_test.sh
./tests/integration/live_three_chain_test.sh
./tests/integration/replay_smoke_test.sh
```

Success is reported only after the transaction receipt contains the expected
`DirectVerificationSucceeded` or `PathVerificationSucceeded`,
`DependencyRecorded`, and `RequestResolved` events; the Gateway getter is true;
and the exact resulting edge is visible through another MapNode's TrustView.

## Explicit limits

- `ExperimentalCostedDirectVerifier` simulates the calibrated verification
  cost with real signatures; it does not implement PoW SPV security.
- The system verifies dependency evidence. It does not custody or transfer
  assets and must not be treated as a bridge.
- Transaction execution is intentionally serialized. Production fee bumping,
  multi-sender nonce management, and third-party replacement policy are out of
  the experiment boundary.
- P2P discovers evidence only. Geth, canonical receipts, deployed bytecode, and
  Gateway state remain authoritative.
- The 21-chain artifact is static render/config validation, not a 21-chain live
  performance result.
- Replay is a deterministic cost-estimation experiment, not a claim that each
  trace message executed SPV or an on-chain transaction. The calibrated profile
  is sensitivity analysis; only `legacy-v4.1` is the legacy reproduction.
