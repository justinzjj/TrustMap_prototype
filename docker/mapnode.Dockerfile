ARG GO_IMAGE=golang:1.24.10-bookworm
ARG DISTROLESS_IMAGE=gcr.io/distroless/static-debian12:nonroot

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/mapnode ./cmd/mapnode
COPY internal/mapnodebootstrap ./internal/mapnodebootstrap
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mapnode ./cmd/mapnode \
    && mkdir -p /out/runtime/data

FROM ${DISTROLESS_IMAGE}
COPY --from=build --chown=nonroot:nonroot /out/mapnode /mapnode
COPY --from=build --chown=nonroot:nonroot /out/runtime /runtime
USER nonroot:nonroot
EXPOSE 8080 9000
HEALTHCHECK --interval=5s --timeout=3s --retries=12 CMD ["/mapnode", "--healthcheck", "http://127.0.0.1:8080/health/ready"]
ENTRYPOINT ["/mapnode"]
