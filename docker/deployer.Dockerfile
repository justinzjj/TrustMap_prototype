ARG JQ_IMAGE=ghcr.io/jqlang/jq:1.8.1
ARG FOUNDRY_IMAGE=ghcr.io/foundry-rs/foundry:v1.4.4

FROM ${JQ_IMAGE} AS jq
FROM ${FOUNDRY_IMAGE}

USER root
COPY --from=jq /jq /usr/local/bin/jq
COPY contracts /workspace/contracts
COPY scripts/deploy-chain.sh /usr/local/bin/deploy-chain
RUN chmod 0555 /usr/local/bin/jq /usr/local/bin/deploy-chain
WORKDIR /workspace/contracts
ENTRYPOINT ["/usr/local/bin/deploy-chain"]
