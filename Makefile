SHELL := /bin/sh

.DEFAULT_GOAL := check

.PHONY: fmt fmt-check test vet check

fmt:
	go fmt ./...

fmt-check:
	@set -eu; \
	unformatted="$$(gofmt -l $$(find . -type f -name '*.go' -not -path './.git/*' -not -path './runtime/*'))"; \
	if [ -n "$$unformatted" ]; then \
		printf '%s\n' "$$unformatted"; \
		exit 1; \
	fi

test:
	go test ./...

vet:
	go vet ./...

check: fmt-check test vet
	@set -eu; \
	if [ -f contracts/foundry.toml ]; then \
		forge fmt --root contracts --check; \
		forge test --root contracts; \
	fi
