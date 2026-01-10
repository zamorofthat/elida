.PHONY: build run test clean docker

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

# Run tests
test:
	go test -v ./...

# Run tests with coverage
test-coverage:
	go test -coverprofile=coverage.out ./...
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
