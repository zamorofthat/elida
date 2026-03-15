# ELIDA Security Controls Reference

**For Auditors, Compliance Teams, and Security Architects**

This document describes the security controls implemented by ELIDA for AI agent traffic. Controls are mapped to industry frameworks including OWASP LLM Top 10 and NIST AI RMF.

---

## Executive Summary

ELIDA provides a security control plane for AI agent traffic with:

- **42+ built-in security rules** covering OWASP LLM Top 10 categories
- **Three enforcement levels**: Flag (audit), Block (reject), Terminate (kill session)
- **Audit mode** for dry-run evaluation without enforcement
- **Complete session records** for compliance and forensics
- **Real-time streaming protection** with configurable latency tradeoffs

---

## Framework Coverage

### OWASP LLM Top 10 (2025)

| ID | Vulnerability | Coverage | Default Action |
|----|--------------|----------|----------------|
| LLM01 | Prompt Injection | **Full** | Block/Terminate |
| LLM02 | Insecure Output Handling | **Full** | Flag (configurable) |
| LLM04 | Model Denial of Service | **Full** | Block |
| LLM05 | Supply Chain Vulnerabilities | **Partial** | Block (model filtering) |
| LLM06 | Sensitive Information Disclosure | **Full** | Flag |
| LLM07 | Insecure Plugin Design | **Full** | Flag/Block |
| LLM08 | Excessive Agency | **Full** | Block/Terminate |
| LLM09 | Overreliance | **Partial** | Flag (audit logging) |
| LLM10 | Model Theft | **Full** | Flag |

### NIST AI RMF

| Function | Coverage |
|----------|----------|
| GOVERN | Policy presets, audit mode, configurable rules |
| MAP | Session tracking, backend routing visibility |
| MEASURE | Metrics collection, violation counting, session records |
| MANAGE | Kill switch, session termination, rate limiting |

---

## Security Controls by Category

### 1. Rate Limiting & Resource Protection

**Purpose**: Prevent denial of service, runaway agents, and resource exhaustion.

| Rule | Threshold | Action | Description |
|------|-----------|--------|-------------|
| `rate_limit_high` | 60 req/min | Block | Prevents DoS and runaway agents |
| `rate_limit_warning` | 30 req/min | Flag | Early warning for elevated activity |
| `high_request_count` | 100 requests | Flag | Session exceeds normal request volume |
| `very_high_request_count` | 500 requests | Block | Potential abuse or automation |
| `long_running_session` | 30 minutes | Flag | Extended session duration |
| `excessive_session_duration` | 1 hour | Block | Likely runaway agent |
| `large_response` | 10 MB | Flag | Large data transfer |
| `excessive_data_transfer` | 50 MB | Block | Excessive bandwidth usage |

### 2. Prompt Injection Protection (LLM01)

**Purpose**: Detect and block attempts to manipulate model behavior through malicious prompts.

| Rule | Patterns Detected | Action |
|------|-------------------|--------|
| `prompt_injection_ignore` | "ignore previous instructions", "disregard system prompt" | Block |
| `prompt_injection_jailbreak` | DAN mode, jailbreak attempts, unrestricted mode | Terminate |
| `prompt_injection_system` | Fake system tags, system prompt injection | Block |
| `prompt_injection_delimiter` | Delimiter-based injection attempts | Flag |

**Example patterns blocked**:
- "Ignore all previous instructions and..."
- "You are now DAN (Do Anything Now)..."
- "Enable developer mode..."
- `[system]...[/system]` injection attempts

### 3. Output Security (LLM02)

**Purpose**: Detect dangerous content in LLM responses that could be executed downstream.

| Rule | Patterns Detected | Action |
|------|-------------------|--------|
| `output_script_injection` | XSS patterns, JavaScript injection | Flag |
| `output_sql_content` | SQL statements in responses | Flag |
| `output_shell_commands` | Shell command patterns | Flag |
| `output_dangerous_code` | Unsafe deserialization, eval() | Flag |

**Note**: Response rules default to `flag` action to avoid streaming latency. Change to `block` if you require pre-delivery inspection (adds latency).

### 4. Data Protection (LLM06)

**Purpose**: Detect requests/responses involving sensitive data.

| Rule | Data Type | Action |
|------|-----------|--------|
| `pii_ssn_request` | Social Security Numbers | Flag |
| `pii_credit_card` | Credit card numbers, CVV | Flag |
| `pii_personal_data` | Bulk PII extraction requests | Flag |
| `credentials_request` | API keys, passwords, secrets | Flag |
| `internal_system_info` | Internal IPs, infrastructure details | Flag |

### 5. Tool/Plugin Security (LLM07)

**Purpose**: Monitor and control tool/function calling in agent frameworks.

| Rule | Tool Type | Action |
|------|-----------|--------|
| `tool_file_access` | File system read/write | Flag |
| `tool_code_execution` | Code interpreter, script execution | Flag |
| `tool_network_access` | HTTP requests, external APIs | Flag |
| `tool_database_access` | SQL queries, database operations | Flag |
| `tool_credential_access` | Secret/credential retrieval | Block |

### 6. Excessive Agency Prevention (LLM08)

**Purpose**: Prevent agents from performing dangerous system operations.

| Rule | Operation | Action |
|------|-----------|--------|
| `shell_execution` | Bash/shell command execution | Block |
| `destructive_file_ops` | rm -rf, format disk, wipe data | Terminate |
| `privilege_escalation` | sudo, root access, chmod 777 | Block |
| `network_exfiltration` | Reverse shells, data exfiltration | Terminate |
| `network_scanning` | Port scanning, network enumeration | Flag |
| `sql_injection` | DROP TABLE, SQL injection patterns | Terminate |

### 7. Model Protection (LLM10)

**Purpose**: Detect attempts to extract model internals or training data.

| Rule | Attack Type | Action |
|------|-------------|--------|
| `model_extraction` | Weight/parameter extraction requests | Flag |
| `training_data_extraction` | Training data extraction attempts | Flag |
| `model_replication` | Model cloning/distillation requests | Flag |
| `systematic_probing` | Brute force enumeration attempts | Flag |

---

## Enforcement Actions

| Action | Behavior | Use Case |
|--------|----------|----------|
| **Flag** | Log violation, continue processing, capture for review | Monitoring, audit trail, low-risk patterns |
| **Block** | Reject request/response with 403, session continues | High-risk patterns, policy violations |
| **Terminate** | Block + kill session permanently | Critical security violations, malicious activity |

---

## Audit Mode

ELIDA supports **audit mode** for evaluating rules without enforcement:

```yaml
policy:
  mode: audit  # Log violations but don't block
```

In audit mode:
- All rules are evaluated
- Violations are logged with `(audit-only)` suffix
- No requests are blocked
- No sessions are terminated
- Session records capture all flagged content

**Recommended**: Run in audit mode for 1-2 weeks before enabling enforcement.

---

## Session Records

Every session generates a complete record including:

| Field | Description |
|-------|-------------|
| `session_id` | Unique session identifier |
| `client_addr` | Client IP address |
| `start_time` / `end_time` | Session duration |
| `request_count` | Total requests in session |
| `bytes_in` / `bytes_out` | Data transfer volume |
| `state` | Final state (Completed, Killed, Terminated, TimedOut) |
| `violations` | List of policy violations |
| `captured_content` | Request/response bodies for flagged sessions |
| `backends_used` | Which LLM backends were accessed |

Session records are:
- Exported via OpenTelemetry (OTLP/Jaeger/Datadog)
- Stored in SQLite for dashboard access
- Exported immediately on kill/terminate (not delayed)

---

## Policy Presets

Three built-in presets provide different security levels:

| Preset | Rules | Use Case |
|--------|-------|----------|
| **minimal** | 8 | Development, testing, low-risk environments |
| **standard** | 38 | Production default, balanced security |
| **strict** | 46 | High-security environments, regulated industries |

```yaml
policy:
  preset: standard  # minimal, standard, or strict
  rules: []         # Custom rules appended to preset
```

---

## Streaming Response Protection

For streaming responses (SSE/NDJSON), ELIDA offers two scanning modes:

| Mode | Latency | Detection | Use Case |
|------|---------|-----------|----------|
| **chunked** | Low | ~99% (overlap buffer) | Default, real-time termination |
| **buffered** | High | 100% | Maximum security, accepts latency |

```yaml
policy:
  streaming:
    mode: chunked      # or buffered
    overlap_size: 1024 # bytes for cross-chunk patterns
```

**Chunked mode**: Scans each chunk with a 1KB overlap buffer to catch patterns spanning chunk boundaries. Terminates stream immediately on detection.

**Buffered mode**: Accumulates entire response before sending. Guarantees pattern detection but adds latency.

**Additional buffered mode use cases**:
- Model output validation before delivery
- Response format verification (JSON schema compliance)
- Quality assurance scoring
- Hallucination detection (future: webhook integration)
- A/B testing without client impact

**Enterprise/Inference Provider use case**: Inference companies can deploy ELIDA as a quality gate to validate model outputs before delivery to customers - ensuring SLA compliance, filtering hallucinations, and maintaining output quality standards.

---

## Compliance Mapping

### SOC 2

| Trust Principle | ELIDA Control |
|-----------------|---------------|
| Security | Rate limiting, input validation, output scanning |
| Availability | DoS protection, runaway session termination |
| Confidentiality | PII detection, credential monitoring |
| Processing Integrity | Policy enforcement, audit logging |
| Privacy | Data flow visibility, content capture |

### GDPR

| Requirement | ELIDA Support |
|-------------|---------------|
| Data minimization | Configurable capture limits |
| Purpose limitation | Session-scoped tracking |
| Audit trail | Complete session records |
| Data subject rights | Session lookup by client |

### HIPAA

| Safeguard | ELIDA Control |
|-----------|---------------|
| Access controls | Session-based tracking |
| Audit controls | OpenTelemetry export, session records |
| Transmission security | TLS support, content inspection |
| Integrity controls | Policy enforcement |

---

## Configuration for Auditors

Recommended audit configuration:

```yaml
policy:
  enabled: true
  mode: audit           # Start with audit mode
  preset: strict        # Maximum visibility
  capture_flagged: true # Capture content for review
  max_capture_size: 50000  # 50KB per request

telemetry:
  enabled: true
  exporter: otlp
  endpoint: "your-collector:4317"

storage:
  enabled: true
  retention_days: 90    # Compliance retention
```

---

## Contacts

For security questions or to report vulnerabilities:
- Security team: [your-security-contact]
- GitHub Issues: [your-repo]/issues

---

*Document version: 1.0*
*Last updated: January 2026*
