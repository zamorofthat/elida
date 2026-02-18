#!/bin/bash
# Chaos Suite Runner - Benchmark policy engine with known-bad prompts
#
# Usage:
#   ./scripts/chaos.sh                  # Run all chaos tests
#   ./scripts/chaos.sh -v               # Verbose output
#   ./scripts/chaos.sh -category jailbreak  # Run specific category
#   ./scripts/chaos.sh -preset strict   # Use strict preset

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}=== ELIDA Chaos Suite ===${NC}"
echo "Benchmarking policy engine with known-bad prompts"
echo ""

# Parse arguments
VERBOSE=""
CATEGORY=""
PRESET="standard"
TEST_ARGS=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -v|--verbose)
            VERBOSE="-v"
            shift
            ;;
        -category|--category)
            CATEGORY="$2"
            shift 2
            ;;
        -preset|--preset)
            PRESET="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 [options]"
            echo ""
            echo "Options:"
            echo "  -v, --verbose         Verbose test output"
            echo "  -category CATEGORY    Run specific category (prompt_injection, jailbreak, tool_abuse, benign)"
            echo "  -preset PRESET        Use policy preset (standard, strict)"
            echo "  -h, --help           Show this help"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Change to project root
cd "$PROJECT_ROOT"

# Build test arguments
if [ -n "$CATEGORY" ]; then
    case $CATEGORY in
        prompt_injection)
            TEST_ARGS="-run TestChaos_PromptInjection"
            ;;
        jailbreak)
            TEST_ARGS="-run TestChaos_Jailbreak"
            ;;
        tool_abuse)
            TEST_ARGS="-run TestChaos_ToolAbuse"
            ;;
        benign)
            TEST_ARGS="-run TestChaos_Benign"
            ;;
        *)
            echo -e "${YELLOW}Unknown category: $CATEGORY${NC}"
            echo "Available: prompt_injection, jailbreak, tool_abuse, benign"
            exit 1
            ;;
    esac
    echo -e "Category: ${YELLOW}$CATEGORY${NC}"
else
    case $PRESET in
        strict)
            TEST_ARGS="-run TestChaos_StrictPreset"
            ;;
        standard|*)
            TEST_ARGS="-run TestChaos_StandardPreset"
            ;;
    esac
    echo -e "Preset: ${YELLOW}$PRESET${NC}"
fi

echo ""

# Run tests
echo -e "${BLUE}Running chaos tests...${NC}"
echo ""

if go test $VERBOSE ./test/chaos/... $TEST_ARGS 2>&1; then
    echo ""
    echo -e "${GREEN}Chaos suite completed successfully!${NC}"
else
    echo ""
    echo -e "${RED}Chaos suite found issues.${NC}"
    echo "Review the output above for details on failures."
    exit 1
fi

# Summary
echo ""
echo -e "${BLUE}=== Summary ===${NC}"
echo "For detailed metrics, run with -v flag"
echo ""
echo "Categories tested:"
echo "  - prompt_injection: Instruction override attempts"
echo "  - jailbreak: Safety bypass attempts"
echo "  - tool_abuse: Malicious tool/function calls"
echo "  - data_exfiltration: Data leakage attempts"
echo "  - recursive_delegation: Runaway agent patterns"
echo "  - benign: False positive tests (should NOT trigger)"
