# Kubernetes Sidecar

Run ELIDA as a sidecar container in the same pod as your agent. Because containers in a pod share a network namespace, your app reaches ELIDA over `localhost` and ELIDA proxies out to the model API. Every agent pod gets its own inline governance layer with no separate service to route through.

```
┌───────────────────────── Pod ─────────────────────────┐
│  agent container ──localhost:8080──▶ elida (sidecar) ──┼──▶ api.anthropic.com
│  OPENAI_BASE_URL / ANTHROPIC_BASE_URL = localhost:8080 │
└────────────────────────────────────────────────────────┘
                         elida control :9090
```

Prefer a sidecar when governance should scale with the workload and share its lifecycle. Prefer the standalone [Helm chart](https://github.com/zamorofthat/elida/tree/main/deploy/helm/elida) when you want one shared, centrally-managed ELIDA fleet.

## Pod spec

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: agent-with-elida
  labels:
    app: agent
spec:
  containers:
    # Your AI agent — talks to ELIDA over localhost
    - name: agent
      image: your-registry/your-agent:latest
      env:
        - name: OPENAI_BASE_URL
          value: "http://localhost:8080"
        # For Anthropic-based agents use:
        # - name: ANTHROPIC_BASE_URL
        #   value: "http://localhost:8080"

    # ELIDA sidecar — inspects traffic, proxies to the real backend
    - name: elida
      image: zamorofthat/elida:latest
      ports:
        - name: proxy
          containerPort: 8080
        - name: control
          containerPort: 9090
      env:
        - name: ELIDA_LISTEN
          value: ":8080"
        - name: ELIDA_CONTROL_LISTEN
          value: ":9090"
        - name: ELIDA_BACKEND
          value: "https://api.anthropic.com"
        - name: ELIDA_POLICY_ENABLED
          value: "true"
        - name: ELIDA_POLICY_PRESET
          value: "standard"
        - name: ELIDA_CONTROL_API_KEY
          valueFrom:
            secretKeyRef:
              name: elida-control
              key: api-key
      livenessProbe:
        httpGet:
          path: /control/health
          port: control
        initialDelaySeconds: 5
        periodSeconds: 10
      readinessProbe:
        httpGet:
          path: /control/health
          port: control
        initialDelaySeconds: 3
        periodSeconds: 5
      resources:
        requests:
          cpu: "50m"
          memory: "64Mi"
        limits:
          cpu: "500m"
          memory: "256Mi"
```

Create the control-API secret first:

```bash
kubectl create secret generic elida-control \
  --from-literal=api-key="$(openssl rand -hex 24)"
```

## Why this works

- **Shared network namespace.** The agent and ELIDA share `localhost`, so `http://localhost:8080` from the agent hits the ELIDA sidecar directly — no Service, no cross-pod hop.
- **The control port is reachable cluster-wide, so the API key is mandatory.** `ELIDA_CONTROL_LISTEN: ":9090"` binds every interface in the pod's namespace, which is what the kubelet probes need. The `containerPort` entry is informational only and blocks nothing, so under a default CNI any pod in the cluster can reach `<podIP>:9090` without a Service. That is why `ELIDA_CONTROL_API_KEY` is set above rather than left out, and ELIDA will refuse to start on a non-loopback control bind with no authentication. To actually restrict reachability, apply a `NetworkPolicy` that denies ingress to port 9090 except from the namespaces or pods that operate ELIDA; `kubectl exec` and `kubectl port-forward` then remain your normal access paths.
- **Egress lock-down still matters.** A sidecar only governs traffic the agent *sends to it*. Use a `NetworkPolicy` to block the agent container from reaching model APIs directly, so `localhost:8080` is its only path out. See [Security Limitations §5](https://github.com/zamorofthat/elida/blob/main/SECURITY_LIMITATIONS.md#5-compensating-controls).

## Native sidecar (init container)

On Kubernetes 1.29+ you can declare ELIDA as a native sidecar (a restartable init container) so it starts before the agent and shuts down after it:

```yaml
spec:
  initContainers:
    - name: elida
      image: zamorofthat/elida:latest
      restartPolicy: Always   # makes this a native sidecar
      ports:
        - name: proxy
          containerPort: 8080
        - name: control
          containerPort: 9090
      env:
        - name: ELIDA_BACKEND
          value: "https://api.anthropic.com"
        - name: ELIDA_CONTROL_API_KEY   # still required — see "Why this works"
          valueFrom:
            secretKeyRef:
              name: elida-control
              key: api-key
```

## Verify

```bash
kubectl apply --dry-run=client -f agent-with-elida.yaml   # validate the manifest
kubectl apply -f agent-with-elida.yaml
kubectl wait --for=condition=Ready pod/agent-with-elida --timeout=60s
kubectl exec agent-with-elida -c elida -- \
  wget -qO- http://localhost:9090/control/health          # expect: healthy response
```

## Related

- [Enterprise Deployment](../enterprise-deployment.md) — standalone Helm chart and fleet management
- [Claude Code integration](claude-code.md)
- [OpenAI SDK integration](openai-sdk.md)
- [Security Limitations](https://github.com/zamorofthat/elida/blob/main/SECURITY_LIMITATIONS.md)
