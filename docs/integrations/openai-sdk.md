# OpenAI SDK

Any tool built on the OpenAI SDK (or an OpenAI-compatible API) can be routed through ELIDA by overriding the base URL. ELIDA proxies to your real backend while tracking sessions and enforcing policy.

```
Your app (OpenAI SDK) → ELIDA (:8080) → api.openai.com (or any OpenAI-compatible backend)
```

## Quick start

Start ELIDA pointed at your backend:

```bash
export ELIDA_CONTROL_API_KEY="$(openssl rand -hex 24)"

docker run -p 8080:8080 -p 127.0.0.1:9090:9090 \
  -e ELIDA_BACKEND=https://api.openai.com/v1 \
  -e ELIDA_CONTROL_API_KEY \
  zamorofthat/elida:latest
```

The control API key is required, not optional. Inside the container ELIDA binds the control port to all interfaces, and it refuses to start on a non-loopback control bind with no authentication — without a key the container exits immediately. Setting `ELIDA_CONTROL_API_KEY` enables auth automatically, and control API calls then need that key as a `Bearer` token.

### Option A: environment variable (no code change)

```bash
export OPENAI_BASE_URL=http://localhost:8080
python my_agent.py
```

### Option B: `base_url` in code

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080",   # point at ELIDA
    api_key="sk-...",                    # your real key; ELIDA forwards it
)

resp = client.chat.completions.create(
    model="gpt-4",
    messages=[{"role": "user", "content": "Hello"}],
)
print(resp.choices[0].message.content)
```

## Correlate requests into a session

ELIDA resolves a session identity in a fixed order, and only the first step is under your explicit control:

1. the `X-Session-ID` header, if present;
2. otherwise an ID derived from the request body — a configured dot-path first (`session.derive_from.body_path`), then the standard OpenAI `user` field (`session.derive_from.openai_user`, on by default);
3. otherwise, while `session.generate_if_missing` is on (the default), a fallback keyed on **client IP plus backend**.

Two consequences are worth knowing. The IP fallback is not per-connection or per-process, so several agent processes behind one source IP talking to the same backend are collapsed into a single session. And if you set `user` in the request body for your own accounting, that value silently becomes the session identity whenever no header is sent.

To tie a whole agent run to one Session Detail Record, send a stable session ID via `default_headers` and stop relying on either fallback:

```python
client = OpenAI(
    base_url="http://localhost:8080",
    api_key="sk-...",
    default_headers={"X-Session-ID": "agent-run-2026-07-06-001"},
)
```

Every call this client makes now shares one session in the dashboard and CDR.

## Streaming

Streaming works unchanged — ELIDA scans chunks incrementally in its default `chunked` mode (see [Security Limitations §6](https://github.com/zamorofthat/elida/blob/main/SECURITY_LIMITATIONS.md#6-performance-notes)):

```python
stream = client.chat.completions.create(
    model="gpt-4",
    messages=[{"role": "user", "content": "Stream this"}],
    stream=True,
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="")
```

## Verify it's working

```bash
curl -s -H "Authorization: Bearer $ELIDA_CONTROL_API_KEY" \
  http://localhost:9090/control/sessions | jq '.sessions'
```

You should see your `X-Session-ID` (or a derived/fallback one) among the active sessions. The endpoint returns `{total, sessions[]}`, so index into `.sessions`, not the top-level value.

## Other OpenAI-compatible backends

`ELIDA_BACKEND` can point at any OpenAI-compatible endpoint — Groq, Mistral, a local Ollama, or an aggregator like LiteLLM. For example, for Groq:

```bash
-e ELIDA_BACKEND=https://api.groq.com/openai/v1
```

See the [Integrations overview](../integrations.md) for putting ELIDA in front of LiteLLM or Portkey.

## Troubleshooting

- **SDK ignores `OPENAI_BASE_URL`** — older SDK versions read `OPENAI_API_BASE`; prefer the explicit `base_url=` constructor argument.
- **`401`** — ELIDA forwards your `Authorization` header verbatim; a 401 means the key is missing or wrong, not an ELIDA issue.
- **Trailing `/v1`** — match your backend's expectation. Point `ELIDA_BACKEND` at the versioned root (`.../v1`) and give the SDK ELIDA's bare origin (`http://localhost:8080`).

## Related

- [Claude Code integration](claude-code.md)
- [Kubernetes sidecar](kubernetes-sidecar.md)
- [Integrations overview](../integrations.md)
- [Security Limitations](https://github.com/zamorofthat/elida/blob/main/SECURITY_LIMITATIONS.md)
