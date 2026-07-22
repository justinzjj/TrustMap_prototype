# TrustMap Prototype

TrustMap is an experimental cross-chain dependency-verification prototype. A
MapNode follows confirmed Gateway events, exchanges validated dependency
evidence, maintains a durable `TrustView`, chooses `DirectPlan` or `PathPlan`,
and submits the selected proof to its home-chain Gateway.

The default `pow-spv-3m` verifier is a real-signature, calibrated cost simulator.
It is not a PoW light client, this repository is not an asset bridge, and the
prototype is not production security infrastructure.

## Prerequisites

- Go 1.24.10
- Docker with Compose v2
- Foundry 1.4.4 for contract-only development

All container images and toolchain versions used by the topology are pinned.

## Validate and render a topology

```sh
go run ./cmd/trustmapctl topology validate --file configs/topology.yaml
go run ./cmd/trustmapctl topology render \
  --file configs/topology.yaml \
  --output runtime/generated
docker compose -f runtime/generated/compose.yaml config --quiet
```

`configs/topology-2.yaml` is the two-chain Direct experiment,
`configs/topology.yaml` is the three-chain recursive Path experiment, and
`configs/topology-21.yaml` proves static render/config support only. The
21-chain topology is not a live load-test claim.

## Run the live experiments

Each script creates a fresh runtime directory, builds and starts real Geth,
deployer, and MapNode containers, uses bounded readiness/request polling, and
always captures Compose logs and removes containers and volumes.

```sh
./tests/integration/live_two_chain_test.sh
./tests/integration/live_three_chain_test.sh
```

The two-chain test verifies a real Direct transaction and the propagated
`B -> A` edge. The three-chain test first establishes `B -> A` and `C -> B`,
then proves that C selects and executes a two-hop Path transaction for A. Both
tests assert the canonical success, dependency, and request-resolution events.

## Development checks

```sh
go test -race ./... -count=1
go vet ./...
CGO_ENABLED=0 go build ./cmd/mapnode
CGO_ENABLED=0 go build ./cmd/trustmapctl
forge test --root contracts -vv
./tests/integration/container_build_test.sh
```

The minimal read-only MapNode API exposes `/health/live`, `/health/ready`,
`/v1/status`, `/v1/requests/{requestId}`, and `/v1/trustview`.

See [docs/traceability.md](docs/traceability.md) for the experiment acceptance
map and current limits.
