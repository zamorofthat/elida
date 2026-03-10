#!/usr/bin/env bash
# Update GitHub Actions to latest SHA-pinned versions
# Usage: ./scripts/update-actions.sh

set -euo pipefail

WORKFLOWS_DIR=".github/workflows"

# Actions to update with their repos
declare -A ACTIONS=(
    ["actions/checkout"]="v4"
    ["actions/setup-go"]="v5"
    ["actions/upload-artifact"]="v4"
    ["actions/download-artifact"]="v4"
    ["golangci/golangci-lint-action"]="v7"
    ["docker/setup-buildx-action"]="v3"
    ["docker/login-action"]="v3"
    ["docker/metadata-action"]="v5"
    ["docker/build-push-action"]="v6"
    ["codecov/codecov-action"]="v4"
    ["trufflesecurity/trufflehog"]="main"
    ["ossf/scorecard-action"]="v2"
    ["github/codeql-action"]="v3"
    ["dtolnay/rust-toolchain"]="master"
    ["Swatinem/rust-cache"]="v2"
    ["softprops/action-gh-release"]="v2"
)

get_latest_sha() {
    local repo=$1
    local ref=$2

    # Try to get tag SHA first, then branch
    SHA=$(gh api "repos/${repo}/git/ref/tags/${ref}" --jq '.object.sha' 2>/dev/null || \
          gh api "repos/${repo}/git/ref/heads/${ref}" --jq '.object.sha' 2>/dev/null || \
          gh api "repos/${repo}/commits/${ref}" --jq '.sha' 2>/dev/null || \
          echo "")

    if [[ -z "$SHA" ]]; then
        echo "Warning: Could not get SHA for ${repo}@${ref}" >&2
        return 1
    fi

    echo "$SHA"
}

get_latest_tag() {
    local repo=$1
    local major=$2

    # Get latest tag matching the major version
    TAG=$(gh api "repos/${repo}/tags" --jq ".[].name" 2>/dev/null | \
          grep -E "^${major}\.[0-9]+(\.[0-9]+)?$" | \
          head -1 || echo "")

    echo "$TAG"
}

echo "Updating GitHub Actions SHA pins..."
echo "======================================"

for action in "${!ACTIONS[@]}"; do
    ref="${ACTIONS[$action]}"
    echo -n "Checking ${action}@${ref}... "

    # Get latest tag if ref is a major version
    if [[ "$ref" =~ ^v[0-9]+$ ]]; then
        latest_tag=$(get_latest_tag "$action" "$ref")
        if [[ -n "$latest_tag" ]]; then
            ref="$latest_tag"
        fi
    fi

    sha=$(get_latest_sha "$action" "$ref")
    if [[ -z "$sha" ]]; then
        echo "SKIP (could not resolve)"
        continue
    fi

    short_sha="${sha:0:40}"
    echo "${ref} -> ${short_sha:0:7}"

    # Update all workflow files
    for file in "$WORKFLOWS_DIR"/*.yml; do
        if [[ -f "$file" ]]; then
            # Match pattern: uses: owner/repo@anything
            # Replace with: uses: owner/repo@sha # version
            sed -i.bak -E "s|uses: ${action}@[a-zA-Z0-9._-]+( # [a-zA-Z0-9._-]+)?|uses: ${action}@${short_sha} # ${ref}|g" "$file"
            rm -f "${file}.bak"
        fi
    done
done

echo ""
echo "Done! Review changes with: git diff .github/workflows/"
