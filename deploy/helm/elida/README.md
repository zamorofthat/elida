# ELIDA Helm Chart

Deploy ELIDA (Edge Layer for Intelligent Defense of Agents) to Kubernetes.

## Prerequisites

- Kubernetes 1.23+
- Helm 3.8+
- (Optional) Redis for horizontal scaling

## Quick Start

```bash
# Add the chart repository (if published)
# helm repo add elida https://charts.elida.dev

# Install with defaults (memory session store, no Redis)
helm install elida ./elida

# Install with Redis for horizontal scaling
helm install elida ./elida --set redis.enabled=true

# Install with custom values
helm install elida ./elida -f my-values.yaml
```

## Configuration

### Key Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of ELIDA replicas | `2` |
| `image.repository` | Container image | `elida` |
| `image.tag` | Image tag | `latest` |
| `elida.listenPort` | Proxy listen port | `8080` |
| `elida.controlPort` | Control API port | `9090` |
| `elida.session.store` | Session store (`memory` or `redis`) | `redis` |
| `elida.session.timeout` | Session timeout | `5m` |
| `elida.tls.enabled` | Enable TLS | `false` |
| `elida.policy.enabled` | Enable policy engine | `true` |
| `elida.storage.enabled` | Enable SQLite storage | `false` |
| `elida.telemetry.enabled` | Enable OpenTelemetry | `false` |
| `redis.enabled` | Deploy Redis sub-chart | `true` |
| `ingress.enabled` | Enable ingress for proxy | `false` |
| `controlIngress.enabled` | Enable ingress for dashboard | `false` |

### Backend Configuration

Configure AI provider backends in `values.yaml`:

```yaml
elida:
  backends:
    anthropic:
      url: "https://api.anthropic.com"
      type: anthropic
      models: ["claude-*"]
      default: true
    openai:
      url: "https://api.openai.com"
      type: openai
      models: ["gpt-*", "o1-*"]
    mistral:
      url: "https://api.mistral.ai"
      type: mistral
      models: ["mistral-*", "codestral-*"]
```

### Session Store Options

**Memory (single instance):**
```yaml
elida:
  session:
    store: memory
redis:
  enabled: false
```

**Redis (horizontal scaling):**
```yaml
elida:
  session:
    store: redis
redis:
  enabled: true
```

**External Redis:**
```yaml
elida:
  session:
    store: redis
redis:
  enabled: false
externalRedis:
  host: "redis.example.com"
  port: 6379
  password: "secret"
```

### TLS Configuration

**Using existing secret:**
```yaml
elida:
  tls:
    enabled: true
    existingSecret: "elida-tls-secret"
```

**Using cert-manager:**
```yaml
ingress:
  enabled: true
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  tls:
    - secretName: elida-tls
      hosts:
        - elida.example.com
```

### Policy Engine

```yaml
elida:
  policy:
    enabled: true
    captureContent: true
    maxCaptureSize: 10000
    rules:
      - name: rate_limit
        type: requests_per_minute
        threshold: 60
        severity: critical
      - name: high_request_count
        type: request_count
        threshold: 100
        severity: warning
```

### Storage (Session History)

```yaml
elida:
  storage:
    enabled: true
    path: /data/elida.db
    size: 5Gi
    storageClass: "gp3"  # AWS EBS
```

### Telemetry (OpenTelemetry)

```yaml
elida:
  telemetry:
    enabled: true
    exporter: otlp
    endpoint: "otel-collector.monitoring:4317"
    serviceName: elida
```

### Autoscaling

```yaml
autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 10
  targetCPUUtilizationPercentage: 80
```

### Ingress

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
  hosts:
    - host: elida.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: elida-tls
      hosts:
        - elida.example.com

controlIngress:
  enabled: true
  className: nginx
  hosts:
    - host: elida-dashboard.example.com
      paths:
        - path: /
          pathType: Prefix
```

## Examples

### Minimal (Development)

```yaml
# dev-values.yaml
replicaCount: 1
elida:
  session:
    store: memory
redis:
  enabled: false
resources:
  limits:
    cpu: 200m
    memory: 128Mi
```

```bash
helm install elida ./elida -f dev-values.yaml
```

### Production

```yaml
# prod-values.yaml
replicaCount: 3

image:
  repository: your-registry.com/elida
  tag: "1.0.0"

elida:
  tls:
    enabled: true
    existingSecret: elida-tls

  policy:
    enabled: true

  storage:
    enabled: true
    size: 10Gi
    storageClass: gp3

  telemetry:
    enabled: true
    endpoint: otel-collector.monitoring:4317

redis:
  enabled: true
  auth:
    enabled: true
    password: "your-redis-password"
  master:
    persistence:
      enabled: true
      size: 1Gi

ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: elida.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: elida-tls
      hosts:
        - elida.example.com

autoscaling:
  enabled: true
  minReplicas: 3
  maxReplicas: 10

podDisruptionBudget:
  enabled: true
  minAvailable: 2

resources:
  limits:
    cpu: 1000m
    memory: 512Mi
  requests:
    cpu: 200m
    memory: 256Mi
```

```bash
helm install elida ./elida -f prod-values.yaml -n elida --create-namespace
```

## Accessing ELIDA

### Port Forward (Development)

```bash
# Proxy
kubectl port-forward svc/elida 8080:8080

# Dashboard
kubectl port-forward svc/elida-control 9090:9090
```

### Configure AI Tools

```bash
export ANTHROPIC_BASE_URL="http://elida.example.com"
export OPENAI_BASE_URL="http://elida.example.com/v1"
export MISTRAL_API_BASE="http://elida.example.com"
```

## Upgrading

```bash
helm upgrade elida ./elida -f values.yaml
```

## Uninstalling

```bash
helm uninstall elida
```

## Troubleshooting

### Pod not starting

```bash
kubectl describe pod -l app.kubernetes.io/name=elida
kubectl logs -l app.kubernetes.io/name=elida
```

### Redis connection issues

```bash
# Check Redis is running
kubectl get pods -l app.kubernetes.io/name=redis

# Test Redis connectivity
kubectl run redis-test --rm -it --image=redis -- redis-cli -h elida-redis-master ping
```

### Health check failing

```bash
kubectl exec -it deploy/elida -- wget -qO- http://localhost:9090/control/health
```

## Chart Structure

```
elida/
├── Chart.yaml           # Chart metadata
├── values.yaml          # Default configuration
├── README.md            # This file
└── templates/
    ├── _helpers.tpl     # Template helpers
    ├── configmap.yaml   # ELIDA configuration
    ├── deployment.yaml  # Main deployment
    ├── hpa.yaml         # Horizontal Pod Autoscaler
    ├── ingress.yaml     # Ingress resources
    ├── NOTES.txt        # Post-install notes
    ├── pdb.yaml         # Pod Disruption Budget
    ├── pvc.yaml         # Persistent Volume Claim
    ├── service.yaml     # Services (proxy + control)
    └── serviceaccount.yaml
```
