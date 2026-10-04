# Claude Code

Route [Claude Code](https://docs.claude.com/claude-code) through ELIDA to get session tracking, policy enforcement on tool calls, a kill switch, and an audit trail of the agent's API traffic — without changing how you use the CLI. ELIDA sees what crosses the wire to Anthropic, not what happens on your filesystem, and it records request and response bodies only when storage capture is turned on (see [Capture the full session for audit](#capture-the-full-session-for-audit)).

## How it works

Claude Code talks to the Anthropic API. Point it at ELIDA instead, and ELIDA proxies to Anthropic while inspecting every request and response on the wire.

```
Claude Code → ELIDA (:8080) → api.anthropic.com
                 │
                 └─ Control API + dashboard (:9090)
```

## Quick start

Start ELIDA pointed at Anthropic:

```bash
docker run -p 8080:8080 -p 127.0.0.1:9090:9090 \
  -e ELIDA_BACKEND=https://api.anthropic.com \
  ghcr.io/zamorofthat/elida:latest
```

Point Claude Code at it:

```bash
ANTHROPIC_BASE_URL=http://localhost:8080 claude
```

Your Anthropic API key still flows through in the `Authorization`/`x-api-key` header — ELIDA passes auth headers straight to the backend and never stores them.

## Verify it's working

While a Claude Code session is active, list sessions on the control API:

```bash
curl -s http://localhost:9090/control/sessions | jq
```

You should see an active session. Open the dashboard at [http://localhost:9090](http://localhost:9090) to watch requests, token burn, and any policy violations in real time.

## Enforce policy on tool calls

Claude Code runs tools (Bash, file edits). Enable a policy preset to inspect and act on them:

```bash
docker run -p 8080:8080 -p 127.0.0.1:9090:9090 \
  -e ELIDA_BACKEND=https://api.anthropic.com \
  -e ELIDA_POLICY_ENABLED=true \
  -e ELIDA_POLICY_PRESET=coding-agent \
  ghcr.io/zamorofthat/elida:latest
```

Use the `coding-agent` preset here. It is tuned for exactly this client: deterministic structural rules (dangerous tool calls, credential-reading tools) enforce, while content and statistical heuristics run in observe mode, flagged and captured but never blocking. A coding agent legitimately emits `bash -c`, `sudo`, `rm -rf` and `curl | sh` in its own output, and its rapid tool loops look like a high-rate, high-entropy burst to an anomaly detector. The `standard` (32 rules) and `strict` (50 rules) presets are the stricter options, and both will false-fire on normal Claude Code traffic.

Whichever preset you pick, start in audit mode (`ELIDA_POLICY_MODE=audit`) to observe flag rates before enforcing — see [Security Limitations](https://github.com/zamorofthat/elida/blob/main/SECURITY_LIMITATIONS.md#4-operational-risks).

## Kill a runaway session

If an agent starts doing something it shouldn't, kill it:

```bash
# Find the session ID — /control/sessions returns {total, sessions[]}
curl -s http://localhost:9090/control/sessions | jq -r '.sessions[0].id'

# Kill it — the next request from that session is refused
curl -X POST http://localhost:9090/control/sessions/<id>/kill
```

## Capture the full session for audit

To record every request and response body (compliance/audit), enable capture-all storage:

```bash
-e ELIDA_STORAGE_ENABLED=true \
-e ELIDA_STORAGE_CAPTURE_MODE=all
```

Persisted Session Detail Records, including the captured bodies, are served from the history endpoint:

```bash
curl -s http://localhost:9090/control/history/<id> | jq
```

`GET /control/sessions/{id}` is a different thing: it reports live metrics for an in-flight session and carries no bodies, and it returns `404` once the session has ended.

## Troubleshooting

- **`connection refused`** — ELIDA isn't listening on `:8080`, or Docker didn't publish the port. Check `curl http://localhost:8080` returns something.
- **`401` from Anthropic** — your API key isn't set in the Claude Code environment. ELIDA forwards whatever auth header it receives and does not inject one, *unless* a backend `api_key` is configured — in which case it overwrites `x-api-key` (Anthropic) or `Authorization` (OpenAI-style) with that key. A backend with an empty `api_key` is also auto-filled from the environment (`<BACKEND_NAME>_API_KEY`, else `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` by backend type), so check for a stale key in ELIDA's own environment before blaming the client.
- **HTTPS backends** — `ELIDA_BACKEND=https://api.anthropic.com` is correct; ELIDA terminates the client's plaintext HTTP and makes its own TLS connection to Anthropic.

## Related

- [OpenAI SDK integration](openai-sdk.md)
- [Kubernetes sidecar](kubernetes-sidecar.md)
- [Integrations overview](../integrations.md)
- [Security Limitations](https://github.com/zamorofthat/elida/blob/main/SECURITY_LIMITATIONS.md)
