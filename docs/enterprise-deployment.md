# ELIDA Enterprise Deployment & Management Guide

## Overview

This guide covers how large organizations deploy, manage, and scale ELIDA across their infrastructure. ELIDA follows the same operational model as telecom Session Border Controllers: a centralized policy enforcement point that sits between AI clients and model backends, providing visibility, control, and security for all AI traffic.

-----

## Deployment Topologies

### Gateway Pattern (Recommended for Most Orgs)

The simplest and most common enterprise deployment. One or more ELIDA instances sit at the network edge between all AI clients and LLM backends.

```
┌─────────────────────────────────────────────────────────┐
│                    Enterprise Network                    │
│                                                         │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐              │
│  │ Dev Team │  │ AI Agents│  │ Internal │              │
│  │ (Claude  │  │ (Auto-   │  │ Apps     │              │
│  │  Code)   │  │  nomous) │  │          │              │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘              │
│       │              │              │                    │
│       └──────────────┼──────────────┘                    │
│                      │                                   │
│              ┌───────▼────────┐                          │
│              │   ELIDA Fleet  │                          │
│              │  (Load Balanced)│                          │
│              │                │                          │
│              │  ┌──────────┐  │                          │
│              │  │ Policy   │  │                          │
│              │  │ Engine   │  │                          │
│              │  ├──────────┤  │                          │
│              │  │ Session  │  │                          │
│              │  │ Store    │◄─┼──── Redis Cluster        │
│              │  ├──────────┤  │                          │
│              │  │ Audit    │  │                          │
│              │  │ Log      │◄─┼──── SQLite / S3          │
│              │  └──────────┘  │                          │
│              └───────┬────────┘                          │
│                      │                                   │
└──────────────────────┼───────────────────────────────────┘
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
   ┌──────────┐ ┌──────────┐ ┌──────────┐
   │ Anthropic│ │  OpenAI  │ │  Ollama  │
   │   API    │ │   API    │ │ (Self-   │
   │          │ │          │ │  hosted) │
   └──────────┘ └──────────┘ └──────────┘
```

**How to distribute to developer machines:**

Developers don't install ELIDA locally — they point their AI tools at the ELIDA gateway by setting environment variables:

```bash
# Claude Code
export ANTHROPIC_BASE_URL=https://elida.internal.company.com

# OpenAI-compatible tools
export OPENAI_BASE_URL=https://elida.internal.company.com/openai

# Or via .env files distributed by your platform team
```

Platform teams can distribute these settings via:

- **MDM profiles** (Jamf, Intune) for managed laptops
- **Developer platform tooling** (Backstage, Port) for standardized environments
- **Shell profiles** (`.bashrc`, `.zshrc`) via dotfiles repos
- **Container base images** for CI/CD and cloud workloads

### Sidecar Pattern (Kubernetes-Native Services)

For organizations running AI-consuming services in Kubernetes, ELIDA can run as a sidecar container alongside each service pod.

```
┌─────────────────────── Pod ───────────────────────┐
│                                                    │
│  ┌──────────────┐         ┌──────────────┐        │
│  │  Your AI     │ ──────► │   ELIDA      │ ────►  LLM Backend
│  │  Service     │ :8080   │   Sidecar    │        │
│  │              │         │              │        │
│  └──────────────┘         └──────────────┘        │
│                                                    │
└────────────────────────────────────────────────────┘
```

This pattern is ideal when:

- Services need per-pod session isolation
- You want ELIDA's policy enforcement tightly coupled to each workload
- Network policies prevent centralized proxying

### Hybrid Pattern

Many large organizations use both: a gateway for developer-facing tools (Claude Code, Cursor, ChatGPT) and sidecars for production AI services running in Kubernetes.

-----

## Kubernetes Deployment with Helm

### Helm Chart

ELIDA ships with a Helm chart in the `deploy/` directory for Kubernetes deployment.

```bash
# Install ELIDA with default configuration
helm install elida ./deploy/helm/elida \
  --namespace elida-system \
  --create-namespace

# Install with custom values
helm install elida ./deploy/helm/elida \
  --namespace elida-system \
  --create-namespace \
  -f my-values.yaml
```

### Example `values.yaml`

```yaml
replicaCount: 3

image:
  repository: ghcr.io/zamorofthat/elida
  tag: latest
  pullPolicy: IfNotPresent

config:
  listen: ":8080"
  control:
    listen: ":9090"
    enabled: true

  # Multi-backend routing
  backends:
    anthropic:
      url: "https://api.anthropic.com"
      type: anthropic
      models: ["claude-*"]
    openai:
      url: "https://api.openai.com"
      type: openai
      models: ["gpt-*", "o1-*"]

  session:
    store: redis
    timeout: 5m
    kill_block:
      mode: duration
      duration: 30m

  policy:
    enabled: true
    preset: standard
    capture_flagged: true

  storage:
    enabled: true
    capture_mode: all

# Redis for horizontal scaling
redis:
  enabled: true
  architecture: replication
  auth:
    enabled: true
    existingSecret: elida-redis-secret

# Autoscaling based on session count
autoscaling:
  enabled: true
  minReplicas: 3
  maxReplicas: 20
  targetCPUUtilizationPercentage: 70

# Ingress for external access
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: elida.internal.company.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: elida-tls
      hosts:
        - elida.internal.company.com

# Service monitor for Prometheus
serviceMonitor:
  enabled: true
  interval: 30s

# Resource limits
resources:
  requests:
    cpu: 250m
    memory: 256Mi
  limits:
    cpu: 1000m
    memory: 1Gi
```

### Sidecar Injection

For the sidecar pattern, add ELIDA as a container in your application's pod spec:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-ai-service
spec:
  template:
    spec:
      containers:
        - name: my-ai-service
          image: my-ai-service:latest
          env:
            - name: ANTHROPIC_BASE_URL
              value: "http://localhost:8080"
        - name: elida
          image: ghcr.io/zamorofthat/elida:latest
          ports:
            - containerPort: 8080
            - containerPort: 9090
          volumeMounts:
            - name: elida-config
              mountPath: /etc/elida
          env:
            - name: ELIDA_BACKEND
              value: "https://api.anthropic.com"
            - name: ELIDA_POLICY_ENABLED
              value: "true"
            - name: ELIDA_POLICY_PRESET
              value: "standard"
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 512Mi
      volumes:
        - name: elida-config
          configMap:
            name: elida-config
```

-----

## Docker Compose (Non-Kubernetes Environments)

For organizations not running Kubernetes, ELIDA's built-in Docker Compose provides a production-ready stack:

```bash
# Start full stack (ELIDA + Redis)
make up

# Or with docker compose directly
docker compose up -d
```

### Production Docker Compose

```yaml
version: "3.8"
services:
  elida:
    image: ghcr.io/zamorofthat/elida:latest
    ports:
      - "8080:8080"   # Proxy
      - "9090:9090"   # Control API + Dashboard
    environment:
      - ELIDA_SESSION_STORE=redis
      - ELIDA_POLICY_ENABLED=true
      - ELIDA_POLICY_PRESET=standard
      - ELIDA_STORAGE_ENABLED=true
      - ELIDA_STORAGE_CAPTURE_MODE=all
      - ELIDA_TELEMETRY_ENABLED=true
    volumes:
      - ./configs/elida.yaml:/etc/elida/elida.yaml
      - elida-data:/var/lib/elida
    depends_on:
      - redis
    restart: unless-stopped
    deploy:
      replicas: 3
      resources:
        limits:
          cpus: "1.0"
          memory: 1G

  redis:
    image: redis:7-alpine
    command: redis-server --appendonly yes --requirepass ${REDIS_PASSWORD}
    volumes:
      - redis-data:/data
    restart: unless-stopped

  # Optional: reverse proxy for TLS termination
  caddy:
    image: caddy:2-alpine
    ports:
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
    depends_on:
      - elida

volumes:
  elida-data:
  redis-data:
```

-----

## Fleet Management

### Centralized Configuration

For managing ELIDA across multiple instances, use a GitOps workflow:

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  Git Repo    │     │   CI/CD      │     │  ELIDA Fleet │
│              │     │              │     │              │
│  policies/   │────►│  Validate &  │────►│  Instance 1  │
│  configs/    │     │  Deploy      │     │  Instance 2  │
│  rules/      │     │              │     │  Instance 3  │
└──────────────┘     └──────────────┘     └──────────────┘
```

**Repository structure for fleet config:**

```
elida-config/
├── base/
│   └── elida.yaml              # Shared base configuration
├── overlays/
│   ├── production/
│   │   ├── elida.yaml          # Production overrides
│   │   └── kustomization.yaml
│   ├── staging/
│   │   └── elida.yaml          # Staging overrides
│   └── dev/
│       └── elida.yaml          # Dev overrides
├── policies/
│   ├── global.yaml             # Org-wide policy rules
│   ├── data-science.yaml       # Team-specific policies
│   ├── customer-support.yaml   # Team-specific policies
│   └── engineering.yaml        # Team-specific policies
└── README.md
```

### Per-Team Policy Scoping

Different teams have different risk profiles. ELIDA's multi-backend routing combined with policy presets allows per-team enforcement:

```yaml
# Engineering team — strict enforcement, all OWASP rules
backends:
  engineering:
    url: "https://api.anthropic.com"
    type: anthropic
    models: ["claude-*"]
    headers:
      match: "X-Team: engineering"

policy:
  enabled: true
  preset: strict
  rules:
    - name: "eng_request_limit"
      type: "request_count"
      threshold: 200
      severity: "warning"

# Data science team — audit mode, higher thresholds
# (separate ELIDA instance or routing rule)
policy:
  enabled: true
  mode: audit    # Log violations but don't block
  preset: standard
  rules:
    - name: "ds_request_limit"
      type: "request_count"
      threshold: 500
      severity: "info"
```

### Configuration Hot-Reload

ELIDA supports environment variable configuration, enabling config changes without restart via Kubernetes ConfigMap updates or Docker secret rotation. For zero-downtime policy updates:

```bash
# Kubernetes: update ConfigMap and trigger rolling restart
kubectl create configmap elida-config \
  --from-file=elida.yaml=configs/elida.yaml \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl rollout restart deployment/elida -n elida-system
```

-----

## Fleet Observability

### OpenTelemetry Integration

ELIDA has built-in OpenTelemetry support for distributed tracing across the fleet:

```yaml
# Enable in elida.yaml
telemetry:
  enabled: true
  endpoint: "otel-collector.monitoring.svc:4317"
  service_name: "elida"
  attributes:
    environment: "production"
    team: "platform"
```

This integrates with your existing observability stack — Grafana, Datadog, Splunk, New Relic, or any OTel-compatible backend.

### Metrics to Monitor Across the Fleet

| Metric | Description | Alert Threshold |
|---|---|---|
| Active sessions per node | Current concurrent sessions | >8,000 (80% of 10K target) |
| Policy violations/min | Rate of OWASP rule triggers | Spike >2x baseline |
| Request latency p99 | Proxy overhead | >200ms |
| Killed sessions | Emergency session terminations | Any (notify SecOps) |
| Memory per node | Session store memory usage | >800MB per node |
| Backend error rate | Upstream LLM failures | >5% |
| Flagged sessions | Sessions with policy violations | Review queue >50 |

### Centralized Dashboard

The ELIDA control API (`:9090`) provides per-instance dashboards. For fleet-wide visibility, aggregate via:

```bash
# Each instance exposes the same control API
curl https://elida-1.internal/control/stats
curl https://elida-2.internal/control/stats
curl https://elida-3.internal/control/stats

# Aggregate in your observability platform via OTel
# or build a fleet dashboard using the control API endpoints:
#   GET /control/stats        — Instance statistics
#   GET /control/sessions     — Active sessions
#   GET /control/flagged      — Policy violations
#   GET /control/history      — Session history
#   GET /control/voice        — Voice sessions (WebSocket)
#   GET /control/voice-history — Voice CDRs
```

### Alerting Integration

Route ELIDA policy violations to your incident response tooling:

```yaml
# Example: OTel Collector config routing ELIDA alerts
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317

processors:
  filter:
    traces:
      span:
        - 'attributes["elida.policy.severity"] == "critical"'

exporters:
  pagerduty:
    routing_key: ${PAGERDUTY_KEY}
  slack:
    webhook_url: ${SLACK_WEBHOOK}
    channel: "#ai-security-alerts"

pipelines:
  traces:
    receivers: [otlp]
    processors: [filter]
    exporters: [pagerduty, slack]
```

-----

## Security Hardening for Enterprise

### Network Architecture

```
Internet ──► WAF ──► Load Balancer ──► ELIDA (TLS) ──► LLM Backends
                                          │
                                          ├──► Redis (private subnet)
                                          └──► SQLite / S3 (audit logs)
```

**Recommendations:**

- **TLS everywhere**: Enable `ELIDA_TLS_ENABLED=true` or terminate TLS at the load balancer
- **Private subnets**: Redis and audit storage should not be internet-accessible
- **API key management**: Store LLM API keys in Kubernetes Secrets or HashiCorp Vault, inject via environment variables
- **Network policies**: Restrict which pods/services can reach ELIDA

### API Key Injection

Never hardcode API keys. Use your secrets management platform:

```yaml
# Kubernetes: mount from Secret
env:
  - name: ANTHROPIC_API_KEY
    valueFrom:
      secretKeyRef:
        name: llm-api-keys
        key: anthropic
  - name: OPENAI_API_KEY
    valueFrom:
      secretKeyRef:
        name: llm-api-keys
        key: openai
```

### Audit & Compliance

ELIDA's capture-all mode provides complete request/response audit trails:

```yaml
storage:
  enabled: true
  capture_mode: "all"              # Capture every request/response
  max_capture_size: 10000          # 10KB per body
  max_captured_per_session: 100    # Max pairs per session
```

For compliance requirements (SOC 2, HIPAA, FedRAMP):

- Enable capture-all mode for complete audit trails
- Ship SQLite history to durable storage (S3, GCS) on a schedule
- Use session kill-block in `permanent` mode for compromised sessions
- Integrate flagged session alerts with your SIEM

-----

## Capacity Planning

### Per-Node Performance

Based on ELIDA benchmarks:

| Metric | Value |
|---|---|
| Memory per session | ~25-30KB (with content capture) |
| Target sessions per node | 10,000 concurrent |
| Projected memory at 10K sessions | ~267MB |
| Proxy latency overhead (enforce mode) | ~113ms avg |
| Blocked request latency | ~49ms (no backend call) |

### Scaling Guidelines

| Org Size | Concurrent AI Users | Recommended Setup |
|---|---|---|
| Small (< 100 devs) | < 500 sessions | 2 ELIDA instances + Redis |
| Medium (100-1,000 devs) | 500-5,000 sessions | 3-5 instances + Redis cluster |
| Large (1,000+ devs) | 5,000-50,000 sessions | 5-20 instances + Redis cluster + HPA |

**Horizontal scaling checklist:**

- [ ] Redis-backed session store (`ELIDA_SESSION_STORE=redis`)
- [ ] Load balancer with session affinity (recommended, not required)
- [ ] HPA configured on CPU and/or custom session-count metric
- [ ] Shared audit storage (S3/GCS) for cross-instance history

-----

## Runbook: Common Enterprise Operations

### Rolling Out ELIDA to a New Team

1. **Create team-specific policy config** in your config repo
2. **Add backend routing** for the team's AI model usage patterns
3. **Distribute environment variables** to the team's machines/services
4. **Start in audit mode** (`ELIDA_POLICY_MODE=audit`) for 1-2 weeks
5. **Review flagged sessions** in the dashboard to tune thresholds
6. **Switch to enforce mode** once policies are calibrated

### Handling a Runaway AI Agent

```bash
# 1. Identify the session in the dashboard
curl https://elida.internal/control/sessions?active=true

# 2. Kill the session immediately
curl -X POST https://elida.internal/control/sessions/{session-id}/kill

# 3. Review what happened
curl https://elida.internal/control/sessions/{session-id}
curl https://elida.internal/control/flagged

# 4. If the agent is compromised, use permanent block
# Configure kill_block.mode: "permanent" for that session class
```

### Upgrading ELIDA Across the Fleet

```bash
# Kubernetes: rolling update
helm upgrade elida ./deploy/helm/elida \
  --namespace elida-system \
  --set image.tag=v1.2.0

# Docker Compose: rolling restart
docker compose pull
docker compose up -d --no-deps --build elida
```

-----

## Roadmap: Enterprise Features

### Available Now

- ✅ Multi-backend routing (header, model, path, default)
- ✅ Redis-backed session store for horizontal scaling
- ✅ 40+ OWASP LLM Top 10 policy rules
- ✅ OpenTelemetry integration
- ✅ Session kill/resume lifecycle
- ✅ Capture-all audit mode
- ✅ WebSocket/voice session tracking
- ✅ Dashboard UI
- ✅ Docker & Docker Compose support

### Planned

- 🔜 Centralized management API (fleet-wide policy push)
- 🔜 RBAC for control API access
- 🔜 Webhook notifications for policy violations
- 🔜 Config hot-reload without restart
- 🔜 Per-team policy scoping via routing rules
- 🔜 S3/GCS audit log shipping
- 🔜 Helm chart improvements (ServiceMonitor, PDB, NetworkPolicy)

### Future (Enterprise Tier)

- 🔮 Fleet management control plane
- 🔮 Centralized dashboard aggregating all instances
- 🔮 SSO/SAML integration for dashboard access
- 🔮 Compliance reporting (SOC 2, HIPAA templates)
- 🔮 Cost analytics per team/agent/model

-----

## Quick Reference

### Environment Variables

| Variable | Default | Description |
|---|---|---|
| `ELIDA_LISTEN` | `:8080` | Proxy listen address |
| `ELIDA_BACKEND` | `http://localhost:11434` | Backend URL |
| `ELIDA_CONTROL_LISTEN` | `:9090` | Control API address |
| `ELIDA_SESSION_STORE` | `memory` | `memory` or `redis` |
| `ELIDA_POLICY_ENABLED` | `false` | Enable policy engine |
| `ELIDA_POLICY_MODE` | `enforce` | `enforce` or `audit` |
| `ELIDA_POLICY_PRESET` | — | `minimal`, `standard`, `strict` |
| `ELIDA_STORAGE_ENABLED` | `false` | Enable SQLite storage |
| `ELIDA_STORAGE_CAPTURE_MODE` | `flagged_only` | `flagged_only` or `all` |
| `ELIDA_WEBSOCKET_ENABLED` | `false` | Enable WebSocket proxy |
| `ELIDA_TLS_ENABLED` | `false` | Enable TLS/HTTPS |
| `ELIDA_TELEMETRY_ENABLED` | `false` | Enable OpenTelemetry |

### Key Control API Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/control/health` | GET | Health check |
| `/control/stats` | GET | Instance statistics |
| `/control/sessions` | GET | List active sessions |
| `/control/sessions/{id}` | GET | Session details |
| `/control/sessions/{id}/kill` | POST | Kill a session |
| `/control/sessions/{id}/resume` | POST | Resume a killed session |
| `/control/flagged` | GET | Policy violations |
| `/control/history` | GET | Session history |
| `/control/voice` | GET | Live voice sessions |
| `/control/voice-history` | GET | Voice CDRs with transcripts |
| `/control/tts` | GET | TTS request tracking |
| `/` | GET | Dashboard UI |
