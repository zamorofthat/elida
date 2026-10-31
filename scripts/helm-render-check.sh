#!/usr/bin/env bash
# Render assertions for the ELIDA Helm chart.
#
# `helm lint` only checks that the templates render. These checks assert what
# the rendered manifests must say about the control API: that it is
# authenticated by default, that the credential travels in a Secret and never
# in the ConfigMap, that the control Service is opt-in, and that a values
# combination the binary would refuse to start on is refused at render time
# instead (internal/config/security.go).
#
# Usage: scripts/helm-render-check.sh [chart-dir]
set -uo pipefail

CHART="${1:-deploy/helm/elida}"
HELM="${HELM:-helm}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# The Redis sub-chart is not needed to render ELIDA's own manifests and would
# require a dependency build.
BASE=(--set redis.enabled=false)

pass=0
fail=0

check() {
  if eval "$2"; then
    echo "  ok   $1"
    pass=$((pass + 1))
  else
    echo "  FAIL $1"
    fail=$((fail + 1))
  fi
}

render() { "$HELM" template r "$CHART" "${BASE[@]}" "$@"; }

# The generated key, decoded, from a rendered Secret.
secret_key() {
  printf '%s' "$1" | awk '/^kind: Secret/,/^---/' |
    grep '  api-key:' | sed 's/.*api-key: //;s/"//g' | base64 -d
}

echo "--- default values: authenticated, credential in a Secret ---"
D=$(render) || { echo "FAIL default render failed"; exit 1; }
check "control auth enabled in config"  'printf "%s" "$D" | grep -A2 "      auth:" | grep -q "enabled: true"'
check "chart-managed Secret created"    'printf "%s" "$D" | grep -qE "^  name: r-elida-control-auth$"'
check "key delivered by secretKeyRef"   'printf "%s" "$D" | grep -q secretKeyRef'
check "no api_key in the ConfigMap"     '! printf "%s" "$D" | grep -q "api_key:"'
check "no control Service"              '! printf "%s" "$D" | grep -qE "^  name: r-elida-control$"'
check "probes are configured"            'printf "%s" "$D" | grep -q livenessProbe && printf "%s" "$D" | grep -q readinessProbe'
KEY=$(secret_key "$D")
check "generated key is 32 chars"       '[ ${#KEY} -eq 32 ]'

echo "--- controlIngress needs the control Service as a backend ---"
I=$(render --set controlIngress.enabled=true)
check "control Service created"         'printf "%s" "$I" | grep -qE "^  name: r-elida-control$"'

echo "--- controlService.enabled=true on its own ---"
C=$(render --set controlService.enabled=true)
check "control Service created"         'printf "%s" "$C" | grep -qE "^  name: r-elida-control$"'

echo "--- existingSecret: the chart manages nothing ---"
E=$(render --set elida.controlAuth.existingSecret=my-secret --set elida.controlAuth.existingSecretKey=k)
check "no chart-managed Secret"         '! printf "%s" "$E" | grep -qE "^  name: r-elida-control-auth$"'
check "reference points at my-secret"   'printf "%s" "$E" | grep -A3 secretKeyRef | grep -q "name: my-secret"'
check "reference uses the given key"    'printf "%s" "$E" | grep -A3 secretKeyRef | grep -q "key: k"'

echo "--- explicit apiKey lands in the Secret, not the ConfigMap ---"
A=$(render --set elida.controlAuth.apiKey=explicit-key)
check "Secret holds the given key"      '[ "$(secret_key "$A")" = "explicit-key" ]'
check "key absent from the ConfigMap"   '! printf "%s" "$A" | grep -q "api_key: \"explicit-key\""'

echo "--- auth off on a pod-reachable bind is refused at render time ---"
if render --set elida.controlAuth.enabled=false >/dev/null 2>"$TMP/err"; then
  echo "  FAIL render should have been refused"
  fail=$((fail + 1))
elif grep -q "refuses to start" "$TMP/err"; then
  echo "  ok   refused, with guidance"
  pass=$((pass + 1))
else
  echo "  FAIL refused for the wrong reason:"
  sed 's/^/       /' "$TMP/err"
  fail=$((fail + 1))
fi

echo "--- auth off with an explicit acknowledgement is permitted ---"
if N=$(render --set elida.controlAuth.enabled=false --set elida.controlAuth.allowInsecure=true 2>&1); then
  check "no Secret"                     '! printf "%s" "$N" | grep -qE "^  name: r-elida-control-auth$"'
  check "no key in the environment"     '! printf "%s" "$N" | grep -q "name: ELIDA_CONTROL_API_KEY"'
  check "allow_insecure recorded"       'printf "%s" "$N" | grep -q "allow_insecure: true"'
else
  echo "  FAIL render failed: $N"
  fail=$((fail + 1))
fi

echo "--- auth off on a loopback bind is permitted ---"
if render --set elida.controlAuth.enabled=false --set elida.controlListen=127.0.0.1:9090 >/dev/null 2>&1; then
  echo "  ok   loopback accepted"
  pass=$((pass + 1))
else
  echo "  FAIL loopback bind refused"
  fail=$((fail + 1))
fi

echo
echo "pass=$pass fail=$fail"
[ "$fail" -eq 0 ]
