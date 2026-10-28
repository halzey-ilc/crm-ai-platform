# syntax=docker/dockerfile:1
# One build stage, one minimal runtime image per binary (select with --target).
# Runtime images are distroless/static: no shell, no package manager, non-root.

FROM golang:1.27.3-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY contracts ./contracts
COPY internal ./internal
COPY services ./services
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -o /out/gateway ./services/gateway && \
    go build -ldflags="-s -w" -o /out/orchestrator ./services/orchestrator && \
    go build -ldflags="-s -w" -o /out/migrate ./services/migrate

FROM gcr.io/distroless/static-debian12:nonroot AS gateway
COPY --from=build /out/gateway /gateway
EXPOSE 8090
USER nonroot:nonroot
HEALTHCHECK --interval=10s --timeout=4s --retries=3 CMD ["/gateway", "-healthcheck", "http://127.0.0.1:8090/health/ready"]
ENTRYPOINT ["/gateway"]

FROM gcr.io/distroless/static-debian12:nonroot AS orchestrator
COPY --from=build /out/orchestrator /orchestrator
EXPOSE 8091
USER nonroot:nonroot
HEALTHCHECK --interval=10s --timeout=4s --retries=3 CMD ["/orchestrator", "-healthcheck", "http://127.0.0.1:8091/health/live"]
ENTRYPOINT ["/orchestrator"]

FROM gcr.io/distroless/static-debian12:nonroot AS migrate
COPY --from=build /out/migrate /migrate
USER nonroot:nonroot
ENTRYPOINT ["/migrate"]
