# ELIDA Client Deployment Guide

This guide covers how to configure AI coding tools to route through ELIDA, and how to deploy these configurations at scale using MDM (Mobile Device Management).

## Overview

```
Developer Workstation                    ELIDA Proxy                    AI Providers
┌─────────────────────┐              ┌─────────────────┐           ┌─────────────────┐
│  Claude Code        │──────────────│                 │───────────│  Anthropic API  │
│  Cursor             │              │    ELIDA        │           │  OpenAI API     │
│  Continue.dev       │──────────────│    :8080        │───────────│  Mistral API    │
│  VS Code + Copilot  │              │                 │           │  Deepseek API   │
│  Custom SDKs        │──────────────│  Session Track  │───────────│  Local Ollama   │
└─────────────────────┘              │  Policy Engine  │           └─────────────────┘
                                     │  Kill Switch    │
                                     └─────────────────┘
```

## Quick Start

Replace `elida.corp.local:8080` with your ELIDA proxy address.

```bash
# Set these environment variables on developer machines
export ANTHROPIC_BASE_URL="http://elida.corp.local:8080"
export OPENAI_BASE_URL="http://elida.corp.local:8080/v1"
export MISTRAL_API_BASE="http://elida.corp.local:8080"
```

---

## AI Tool Configuration

### Claude Code CLI

Claude Code is Anthropic's official CLI tool for AI-assisted coding.

**Option 1: Settings file** (`~/.claude/settings.json`)
```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://elida.corp.local:8080"
  }
}
```

**Option 2: Environment variable**
```bash
export ANTHROPIC_BASE_URL="http://elida.corp.local:8080"
```

**Option 3: Per-session override**
```bash
ANTHROPIC_BASE_URL="http://elida.corp.local:8080" claude
```

---

### Mistral AI

**Environment variable:**
```bash
export MISTRAL_API_BASE="http://elida.corp.local:8080"
```

**Python SDK:**
```python
from mistralai import Mistral

client = Mistral(
    api_key=os.environ["MISTRAL_API_KEY"],
    server_url="http://elida.corp.local:8080"
)
```

**JavaScript SDK:**
```javascript
import Mistral from '@mistralai/mistralai';

const client = new Mistral({
  apiKey: process.env.MISTRAL_API_KEY,
  serverURL: "http://elida.corp.local:8080"
});
```

---

### Continue.dev (VS Code Extension)

Continue is an open-source AI coding assistant for VS Code and JetBrains.

**Config file:** `~/.continue/config.yaml`
```yaml
models:
  - name: "claude-3-opus"
    provider: "anthropic"
    apiBase: "http://elida.corp.local:8080"
    apiKey: "${ANTHROPIC_API_KEY}"

  - name: "gpt-4"
    provider: "openai"
    apiBase: "http://elida.corp.local:8080/v1"
    apiKey: "${OPENAI_API_KEY}"

  - name: "codestral"
    provider: "mistral"
    apiBase: "http://elida.corp.local:8080/v1"
    apiKey: "${MISTRAL_API_KEY}"

  - name: "deepseek-coder"
    provider: "openai"  # Deepseek uses OpenAI-compatible API
    apiBase: "http://elida.corp.local:8080/v1"
    apiKey: "${DEEPSEEK_API_KEY}"
```

---

### Cursor IDE

Cursor is an AI-first code editor.

**Via Settings UI:**
1. Open Cursor
2. Go to Settings → Cursor Settings → Models
3. Enable "Override OpenAI Base URL"
4. Enter: `http://elida.corp.local:8080/v1`
5. Add your API key

**Via settings.json:**
```json
{
  "cursor.aiBaseUrl": "http://elida.corp.local:8080/v1"
}
```

---

### OpenAI SDK (Generic)

Works with any tool using the official OpenAI SDK.

**Environment variable:**
```bash
export OPENAI_BASE_URL="http://elida.corp.local:8080/v1"
```

**Python:**
```python
from openai import OpenAI

client = OpenAI(
    base_url="http://elida.corp.local:8080/v1",
    api_key=os.environ["OPENAI_API_KEY"]
)
```

**Node.js:**
```javascript
import OpenAI from 'openai';

const client = new OpenAI({
  baseURL: "http://elida.corp.local:8080/v1",
  apiKey: process.env.OPENAI_API_KEY
});
```

---

### Aider (AI Pair Programming)

**Environment variable:**
```bash
export OPENAI_API_BASE="http://elida.corp.local:8080/v1"
aider
```

**Or in .aider.conf.yml:**
```yaml
openai-api-base: http://elida.corp.local:8080/v1
```

---

### LangChain

**Python:**
```python
from langchain_openai import ChatOpenAI

llm = ChatOpenAI(
    model="gpt-4",
    base_url="http://elida.corp.local:8080/v1",
    api_key=os.environ["OPENAI_API_KEY"]
)
```

---

## MDM Deployment

### macOS (Jamf, Kandji, Mosyle)

#### Option 1: Configuration Profile

Deploy environment variables via macOS configuration profile.

See: [`examples/macos/elida-proxy.mobileconfig`](examples/macos/elida-proxy.mobileconfig)

**Deploy via Jamf:**
1. Upload the `.mobileconfig` to Jamf Pro
2. Create a Configuration Profile policy
3. Scope to target computers/groups
4. Deploy

**Deploy via Kandji:**
1. Go to Library → Custom Profiles
2. Upload the `.mobileconfig`
3. Assign to Blueprints

#### Option 2: Deployment Script

Run as a login script or via MDM.

See: [`examples/macos/deploy-elida-config.sh`](examples/macos/deploy-elida-config.sh)

**Deploy via Jamf:**
1. Upload script to Jamf Pro → Settings → Scripts
2. Create a Policy with the script
3. Set trigger: Login, Recurring Check-in, or Self Service
4. Scope to target computers

---

### Windows (Intune, SCCM, GPO)

#### Option 1: PowerShell Script (Intune/SCCM)

See: [`examples/windows/Deploy-ElidaConfig.ps1`](examples/windows/Deploy-ElidaConfig.ps1)

**Deploy via Intune:**
1. Go to Devices → Scripts
2. Add new PowerShell script
3. Upload `Deploy-ElidaConfig.ps1`
4. Configure: Run as system, or user context
5. Assign to device groups

**Deploy via SCCM:**
1. Create a Package with the script
2. Create a Program: `powershell.exe -ExecutionPolicy Bypass -File Deploy-ElidaConfig.ps1`
3. Deploy to collection

#### Option 2: Group Policy (GPO)

Set environment variables via GPO.

See: [`examples/windows/elida-gpo-settings.md`](examples/windows/elida-gpo-settings.md)

1. Open Group Policy Management
2. Create new GPO or edit existing
3. Navigate to: Computer Configuration → Preferences → Windows Settings → Environment
4. Add new environment variables

---

### Linux (Ansible, Chef, Puppet)

#### Ansible

See: [`examples/linux/deploy-elida.yml`](examples/linux/deploy-elida.yml)

```bash
ansible-playbook -i inventory deploy-elida.yml
```

#### Chef

See: [`examples/linux/elida-cookbook/`](examples/linux/elida-cookbook/)

#### Shell Script (Generic)

See: [`examples/linux/deploy-elida-config.sh`](examples/linux/deploy-elida-config.sh)

---

## ELIDA Server Configuration

Configure ELIDA to route to all your AI providers:

```yaml
# /etc/elida/elida.yaml (or configs/elida.yaml)
listen: ":8080"

backends:
  anthropic:
    url: "https://api.anthropic.com"
    models: ["claude-*"]
    default: true

  openai:
    url: "https://api.openai.com"
    models: ["gpt-*", "o1-*", "o3-*"]

  mistral:
    url: "https://api.mistral.ai"
    models: ["mistral-*", "codestral-*"]

  deepseek:
    url: "https://api.deepseek.com"
    models: ["deepseek-*"]

  ollama:
    url: "http://localhost:11434"
    models: ["llama*", "qwen*", "phi*"]

routing:
  methods:
    - header    # X-Backend header (highest priority)
    - model     # Match by model name in request body
    - path      # Path prefix (/openai/*, /anthropic/*)
    - default   # Fallback to default backend

control:
  enabled: true
  listen: ":9090"
  auth:
    enabled: true
    api_key: "${ELIDA_CONTROL_API_KEY}"

policy:
  enabled: true
  mode: enforce
  preset: standard

storage:
  enabled: true
  capture_mode: flagged_only

telemetry:
  enabled: true
  exporter: otlp
  endpoint: "otel-collector.corp.local:4317"
```

---

## Verification

After deployment, verify ELIDA is receiving traffic:

```bash
# Check ELIDA is running
curl http://elida.corp.local:9090/control/health

# View active sessions
curl -H "Authorization: Bearer $ELIDA_CONTROL_API_KEY" \
  http://elida.corp.local:9090/control/sessions

# View session statistics
curl -H "Authorization: Bearer $ELIDA_CONTROL_API_KEY" \
  http://elida.corp.local:9090/control/stats
```

**Test from a developer machine:**
```bash
# Should route through ELIDA
curl -X POST http://elida.corp.local:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}]}'
```

---

## Troubleshooting

### Connection refused
- Verify ELIDA is running: `curl http://elida.corp.local:8080/health`
- Check firewall rules allow traffic to port 8080
- Verify DNS resolution: `nslookup elida.corp.local`

### 401 Unauthorized from backend
- API keys are passed through to backends
- Ensure `Authorization` header is set correctly in client
- Check ELIDA logs for forwarded headers

### Environment variables not taking effect
- macOS: Log out and back in after profile deployment
- Windows: Restart applications or sign out/in
- Linux: Source the profile or restart shell

### Sessions not appearing in dashboard
- Verify storage is enabled in ELIDA config
- Check ELIDA has write access to SQLite database path
- Verify control API is accessible

---

## Security Considerations

1. **Use TLS in production**: Configure ELIDA with TLS certificates
   ```yaml
   tls:
     enabled: true
     cert_file: "/etc/elida/cert.pem"
     key_file: "/etc/elida/key.pem"
   ```

2. **Protect control API**: Always enable auth for the control API
   ```yaml
   control:
     auth:
       enabled: true
       api_key: "${ELIDA_CONTROL_API_KEY}"
   ```

3. **Network segmentation**: Place ELIDA in a DMZ or dedicated VLAN

4. **Audit logging**: Enable storage and telemetry for compliance
   ```yaml
   storage:
     enabled: true
     capture_mode: all  # For full audit trail
   ```
