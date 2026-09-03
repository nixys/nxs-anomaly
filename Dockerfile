FROM golang:1.25-bookworm AS build

ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
COPY vendor ./vendor

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath \
    -ldflags="-s -w -X github.com/nixys/nxs-anomaly/internal/server.Version=${VERSION}" \
    -o /out/nxs-anomaly ./cmd/nxs-anomaly

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/nxs-anomaly /usr/local/bin/nxs-anomaly

USER nonroot:nonroot

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/nxs-anomaly", "healthcheck", "--url", "http://127.0.0.1:8080/health"]

CMD ["/usr/local/bin/nxs-anomaly", "serve", "--host", "0.0.0.0", "--port", "8080"]
