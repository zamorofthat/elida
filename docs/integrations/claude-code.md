# Claude Code

Route [Claude Code](https://docs.claude.com/claude-code) through ELIDA to get session tracking, policy enforcement on tool calls, a kill switch, and a full audit trail of everything the agent does in your codebase — without changing how you use the CLI.

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
  -e ELIDA_POLICY_PRESET=standard \
  ghcr.io/zamorofthat/elida:latest
```

Start in audit mode (`ELIDA_POLICY_MODE=audit`) first to observe flag rates before enforcing — see [Security Limitations](../../SECURITY_LIMITATIONS.md#4-operational-risks).

## Kill a runaway session

If an agent starts doing something it shouldn't, kill it:

```bash
# Find the session ID
curl -s http://localhost:9090/control/sessions | jq -r '.[0].id'

# Kill it — the next request from that session is refused
curl -X POST http://localhost:9090/control/sessions/<id>/kill
```

## Capture the full session for audit

To record every request and response body (compliance/audit), enable capture-all storage:

```bash
-e ELIDA_STORAGE_ENABLED=true \
-e ELIDA_STORAGE_CAPTURE_MODE=all
```

Session Detail Records are then available via `GET /control/sessions/{id}`.

## Troubleshooting

- **`connection refused`** — ELIDA isn't listening on `:8080`, or Docker didn't publish the port. Check `curl http://localhost:8080` returns something.
- **`401` from Anthropic** — your API key isn't set in the Claude Code environment; ELIDA forwards whatever auth header it receives, it does not inject one.
- **HTTPS backends** — `ELIDA_BACKEND=https://api.anthropic.com` is correct; ELIDA terminates the client's plaintext HTTP and makes its own TLS connection to Anthropic.

## Related

- [OpenAI SDK integration](openai-sdk.md)
- [Kubernetes sidecar](kubernetes-sidecar.md)
- [Integrations overview](../integrations.md)
- [Security Limitations](../../SECURITY_LIMITATIONS.md)
