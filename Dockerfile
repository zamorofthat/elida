# Build stage - Dashboard
FROM oven/bun:1.4.2-alpine AS dashboard-builder

WORKDIR /app/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

# Build stage - model artifact
#
# The float32 ONNX graph is derived from upstream's int8 publication by
# scripts/models/dequantize.py, which needs Python. Doing it in its own
# stage keeps the Go builder pure: no Python, no pip, nothing but go build.
FROM python:3.13-slim AS model-builder

# The pinned upstream commit (see scripts/models/README.md). fetch.sh refuses
# any value other than the commit its checksums were recorded at, so this
# cannot float; it is required so a build never silently ships no model.
ARG DEFENDER_COMMIT
RUN test -n "${DEFENDER_COMMIT}" || (echo "DEFENDER_COMMIT build-arg is required (see scripts/models/README.md)" && exit 1)

WORKDIR /src

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
# Only onnx and numpy, at exactly the versions build.sh enforces: another
# version may serialize a different graph and change the model.onnx digest.
# Parity is a Go test, so no onnxruntime or tokenizers.
RUN pip install --no-cache-dir 'onnx==1.23.1' 'numpy==2.5.3'

COPY scripts/models/ ./scripts/models/
COPY docs/model-card-injection.md ./docs/model-card-injection.md
RUN DEFENDER_COMMIT="${DEFENDER_COMMIT}" OUT_DIR=/out/injection scripts/models/build.sh \
 && chmod -R a-w /out/injection

# Build stage - Go binary
FROM golang:1.27-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Copy built dashboard into expected location
COPY --from=dashboard-builder /app/web/../internal/dashboard/static ./internal/dashboard/static

# Build binary with embedded dashboard
ARG VERSION=dev
# GOEXPERIMENT=simd turns GoMLX's scalar kernels into the SIMD kernels that
# make inline semantic inference viable. It is gated to amd64 in GoMLX's
# source, so it is derived here from TARGETARCH, which buildx sets
# automatically for each platform it builds. It is not a build-arg: the
# release workflows pass only VERSION, so an arg would be inert and every
# amd64 image would ship scalar kernels.
ARG TARGETARCH
RUN GOEXPERIMENT=$([ "$TARGETARCH" = "amd64" ] && echo simd || true) CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s -X main.Version=${VERSION}" -o elida ./cmd/elida

# Runtime stage
FROM alpine:3.24

# Create non-root user
RUN addgroup -g 1000 elida && \
    adduser -u 1000 -G elida -s /bin/sh -D elida

WORKDIR /app

# Install ca-certificates for HTTPS backends
RUN apk --no-cache add ca-certificates tzdata

# Create data directory
RUN mkdir -p /data && chown elida:elida /data

# Copy binary from builder
COPY --from=builder /app/elida .
COPY --from=builder /app/configs/elida.yaml ./configs/

# Model assets at the default decision.model_path. The directory is
# read-only: ELIDA never downloads or replaces a model at runtime, and a
# writable model directory is a supply-chain hazard, not a convenience.
# The files' write bits are removed in the model-builder stage and COPY
# keeps file modes, so the ~90 MiB layer is written once (a chmod -R here
# would copy every file into a second layer). COPY does not keep the
# destination directory's mode and gives the parents it creates the
# --chown owner, so the RUN below fixes only those directories: the model
# directory loses its write bits, and /etc/elida and /etc/elida/models go
# back to root so the model directory cannot be renamed or replaced.
COPY --from=model-builder --chown=elida:elida /out/injection /etc/elida/models/injection
RUN chown root:root /etc/elida /etc/elida/models \
 && chmod 0555 /etc/elida/models/injection

# Set ownership
RUN chown -R elida:elida /app

# Switch to non-root user
USER elida

# Expose ports
# 8080 - Proxy traffic
# 9090 - Control API / Dashboard
EXPOSE 8080 9090

# Environment variables for configuration
# Control API binds to all interfaces inside the container (required for
# Docker healthcheck and port mapping). Auth is enforced by the security
# validator — set ELIDA_CONTROL_API_KEY or ELIDA_CONTROL_AUTH_ALLOW_INSECURE=true.
ENV ELIDA_LISTEN=:8080
ENV ELIDA_CONTROL_LISTEN=:9090

# Health check — /control/health is exempt from auth
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:9090/control/health || exit 1

# Run
ENTRYPOINT ["./elida"]
CMD ["-config", "configs/elida.yaml"]
