#!/bin/bash
#
# Build and push ELIDA Docker image to Docker Hub
#
# Usage:
#   ./scripts/docker-push.sh                    # Push with 'latest' tag
#   ./scripts/docker-push.sh v1.0.0             # Push with specific version tag
#   ./scripts/docker-push.sh v1.0.0 --latest    # Push version + latest tags
#
# Environment variables:
#   DOCKER_REGISTRY  - Registry to push to (default: docker.io)
#   DOCKER_USERNAME  - Docker Hub username or org
#   DOCKER_IMAGE     - Image name (default: elida)
#
# For CI/CD:
#   Set DOCKER_USERNAME and DOCKER_PASSWORD (or DOCKER_TOKEN) secrets
#

set -euo pipefail

# Configuration
REGISTRY="${DOCKER_REGISTRY:-docker.io}"
USERNAME="${DOCKER_USERNAME:-}"
IMAGE="${DOCKER_IMAGE:-elida}"
VERSION="${1:-latest}"
PUSH_LATEST="${2:-}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Validate configuration
if [ -z "$USERNAME" ]; then
    log_error "DOCKER_USERNAME is required"
    echo "Set it via environment variable or login to Docker Hub first"
    exit 1
fi

FULL_IMAGE="${REGISTRY}/${USERNAME}/${IMAGE}"

log_info "Building Docker image..."
log_info "  Registry: ${REGISTRY}"
log_info "  Image: ${USERNAME}/${IMAGE}"
log_info "  Version: ${VERSION}"

# Get git info for labels
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Build the image with labels
docker build \
    --label "org.opencontainers.image.version=${VERSION}" \
    --label "org.opencontainers.image.revision=${GIT_COMMIT}" \
    --label "org.opencontainers.image.created=${BUILD_DATE}" \
    --label "org.opencontainers.image.source=https://github.com/zamorofthat/elida" \
    --label "org.opencontainers.image.title=ELIDA" \
    --label "org.opencontainers.image.description=Edge Layer for Intelligent Defense of Agents" \
    -t "${FULL_IMAGE}:${VERSION}" \
    .

log_info "Build complete: ${FULL_IMAGE}:${VERSION}"

# Login if credentials provided
if [ -n "${DOCKER_PASSWORD:-}" ]; then
    log_info "Logging in to Docker Hub..."
    echo "${DOCKER_PASSWORD}" | docker login "${REGISTRY}" -u "${USERNAME}" --password-stdin
elif [ -n "${DOCKER_TOKEN:-}" ]; then
    log_info "Logging in to Docker Hub with token..."
    echo "${DOCKER_TOKEN}" | docker login "${REGISTRY}" -u "${USERNAME}" --password-stdin
fi

# Push version tag
log_info "Pushing ${FULL_IMAGE}:${VERSION}..."
docker push "${FULL_IMAGE}:${VERSION}"

# Push latest tag if requested
if [ "${VERSION}" != "latest" ] && [ "${PUSH_LATEST}" == "--latest" ]; then
    log_info "Tagging and pushing as latest..."
    docker tag "${FULL_IMAGE}:${VERSION}" "${FULL_IMAGE}:latest"
    docker push "${FULL_IMAGE}:latest"
fi

log_info "Push complete!"
echo ""
echo "Pull with:"
echo "  docker pull ${FULL_IMAGE}:${VERSION}"
if [ "${PUSH_LATEST}" == "--latest" ]; then
    echo "  docker pull ${FULL_IMAGE}:latest"
fi
