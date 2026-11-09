# etunneld multi-stage build: static Go binary on distroless.
# OAuth/webhook outbound calls need CA certificates, hence nonroot (which
# bundles them) instead of distroless/static.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/etunneld ./cmd/etunneld

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/etunneld /etunneld
# Persistent state (JSON store / ACME cache) lives here; runs as uid 65532.
VOLUME /data
ENV ETUNNEL_DATA_DIR=/data
EXPOSE 8080 7946 7946/udp
USER nonroot
ENTRYPOINT ["/etunneld"]
