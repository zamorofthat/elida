# Build stage - Dashboard
FROM node:20-alpine AS dashboard-builder

WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Build stage - Go binary
FROM golang:1.22-alpine AS builder

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
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o elida ./cmd/elida

# Runtime stage
FROM alpine:3.19

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

# Set ownership
RUN chown -R elida:elida /app

# Switch to non-root user
USER elida

# Expose ports
# 8080 - Proxy traffic
# 9090 - Control API / Dashboard
EXPOSE 8080 9090

# Environment variables for configuration
ENV ELIDA_LISTEN=:8080
ENV ELIDA_CONTROL_LISTEN=:9090

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:9090/control/health || exit 1

# Run
ENTRYPOINT ["./elida"]
CMD ["-config", "configs/elida.yaml"]
