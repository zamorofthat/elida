.PHONY: build run stop restart run-policy run-demo test clean docker

# Build variables
BINARY_NAME=elida
VERSION=0.1.0
BUILD_TIME=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-X main.version=${VERSION} -X main.buildTime=${BUILD_TIME}"

# Build the binary
build:
	go build ${LDFLAGS} -o bin/${BINARY_NAME} ./cmd/elida

# Run locally
run: build
	./bin/${BINARY_NAME} -config configs/elida.yaml

# Run with hot reload (requires air: go install github.com/cosmtrek/air@latest)
dev:
	air

# Stop running ELIDA server
stop:
	@pkill -f "bin/${BINARY_NAME}" 2>/dev/null || echo "ELIDA not running"

# Restart ELIDA server
restart: stop build
	@sleep 0.5
	./bin/${BINARY_NAME} -config configs/elida.yaml

# Run with policy engine enabled
run-policy: build
	ELIDA_POLICY_ENABLED=true ELIDA_POLICY_PRESET=standard ./bin/${BINARY_NAME} -config configs/elida.yaml

# Run with policy + storage (demo mode)
run-demo: build
	ELIDA_POLICY_ENABLED=true ELIDA_POLICY_PRESET=standard ELIDA_STORAGE_ENABLED=true ./bin/${BINARY_NAME} -config configs/elida.yaml

# Run unit tests (no external dependencies)
test:
	go test -v ./test/unit/...

# Run integration tests (requires Redis)
test-integration: redis-up
	go test -v ./test/integration/...

# Run all tests
test-all: redis-up
	go test -v ./test/...

# Run tests with coverage
test-coverage:
	go test -coverprofile=coverage.out ./test/unit/...
	go tool cover -html=coverage.out -o coverage.html

# Run all tests with coverage
test-coverage-all: redis-up
	go test -coverprofile=coverage.out ./test/...
	go tool cover -html=coverage.out -o coverage.html

# Clean build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html

# Format code
fmt:
	go fmt ./...

# Lint code (requires golangci-lint)
lint:
	golangci-lint run

# Download dependencies
deps:
	go mod download
	go mod tidy

# Build Docker image
docker:
	docker build -t elida:${VERSION} .
	docker tag elida:${VERSION} elida:latest

# Run in Docker
docker-run:
	docker run -p 8080:8080 -p 9090:9090 elida:latest

# Quick test against Ollama
test-ollama:
	@echo "Testing against Ollama..."
	curl -s http://localhost:8080/api/tags | jq .

# Test streaming
test-stream:
	@echo "Testing streaming..."
	curl -s http://localhost:8080/api/generate \
		-d '{"model": "qwen:0.5b", "prompt": "Hello", "stream": true}'

# Check active sessions
sessions:
	curl -s http://localhost:9090/control/sessions | jq .

# Check stats
stats:
	curl -s http://localhost:9090/control/stats | jq .

# Health check
health:
	curl -s http://localhost:9090/control/health | jq .

# Start Redis container
redis-up:
	docker compose up -d redis

# Stop Redis container
redis-down:
	docker compose down

# Run with Redis backend
run-redis: build redis-up
	ELIDA_SESSION_STORE=redis ./bin/${BINARY_NAME} -config configs/elida.yaml

# Start full stack (Redis + ELIDA)
up:
	docker compose up -d

# Stop full stack
down:
	docker compose down

# View Redis keys
redis-keys:
	docker compose exec redis redis-cli KEYS "elida:*"

# Flush Redis
redis-flush:
	docker compose exec redis redis-cli FLUSHDB

# Start Jaeger for tracing
jaeger-up:
	docker compose up -d jaeger

# Run with telemetry (stdout exporter for debugging)
run-telemetry: build
	ELIDA_TELEMETRY_ENABLED=true ELIDA_TELEMETRY_EXPORTER=stdout ./bin/${BINARY_NAME} -config configs/elida.yaml

# Run with Jaeger tracing
run-jaeger: build jaeger-up
	ELIDA_TELEMETRY_ENABLED=true ELIDA_TELEMETRY_EXPORTER=otlp ELIDA_TELEMETRY_ENDPOINT=localhost:4317 ./bin/${BINARY_NAME} -config configs/elida.yaml

# Open Jaeger UI
jaeger-ui:
	open http://localhost:16686

# Run with SQLite storage enabled
run-storage: build
	ELIDA_STORAGE_ENABLED=true ELIDA_STORAGE_PATH=data/elida.db ./bin/${BINARY_NAME} -config configs/elida.yaml

# Run with all features (storage + telemetry)
run-full: build jaeger-up
	ELIDA_STORAGE_ENABLED=true ELIDA_STORAGE_PATH=data/elida.db ELIDA_TELEMETRY_ENABLED=true ELIDA_TELEMETRY_EXPORTER=otlp ELIDA_TELEMETRY_ENDPOINT=localhost:4317 ./bin/${BINARY_NAME} -config configs/elida.yaml

# Query session history
history:
	curl -s http://localhost:9090/control/history | jq .

# Query historical stats
history-stats:
	curl -s http://localhost:9090/control/history/stats | jq .

# Query time series data
history-timeseries:
	curl -s http://localhost:9090/control/history/timeseries | jq .

# Install ELIDA as system service (auto-start on boot)
install: build
	./scripts/install.sh install

# Uninstall ELIDA service
uninstall:
	./scripts/install.sh uninstall

# Check service status
service-status:
	./scripts/install.sh status

# Setup environment variables for AI tools
setup-env:
	./scripts/install.sh env
