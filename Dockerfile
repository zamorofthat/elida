# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o elida ./cmd/elida

# Runtime stage
FROM alpine:3.19

WORKDIR /app

# Install ca-certificates for HTTPS backends
RUN apk --no-cache add ca-certificates

# Copy binary from builder
COPY --from=builder /app/elida .
COPY --from=builder /app/configs/elida.yaml ./configs/

# Expose ports
# 8080 - Proxy traffic
# 9090 - Control API
EXPOSE 8080 9090

# Environment variables for configuration
ENV ELIDA_LISTEN=:8080
ENV ELIDA_BACKEND=http://localhost:11434
ENV ELIDA_CONTROL_LISTEN=:9090

# Run
ENTRYPOINT ["./elida"]
CMD ["-config", "configs/elida.yaml"]
