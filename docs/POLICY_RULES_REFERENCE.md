# ELIDA Policy Rules Reference

This document contains the complete set of default policy rules available in ELIDA's presets. These rules are built into the code and activated via the `preset` configuration option.

## Available Presets

| Preset | Description | Use Case |
|--------|-------------|----------|
| `minimal` | Basic rate limiting only | Development/testing |
| `standard` | OWASP LLM Top 10 basics + rate limits | Production default |
| `strict` | Full OWASP + NIST + PII detection | High-security environments |

## Configuration

```yaml
policy:
  enabled: true
  preset: standard  # Use built-in rules

  # Add custom rules (appended to preset):
  rules:
    - name: "my_custom_rule"
      type: "content_match"
      target: "request"
      patterns: ["my-pattern"]
      severity: "warning"
      action: "flag"
      description: "My custom rule"
```

## Rule Target Options

| Target | Description | Use Case |
|--------|-------------|----------|
| `request` | Only scan request bodies | Prompt injection, malicious input |
| `response` | Only scan response bodies | XSS, dangerous code in output |
| `both` | Scan both (default if omitted) | PII, credentials |

## Streaming Response Behavior

For response-side rules with streaming responses:
- **`flag` action**: Response streams normally, async scan after delivery (no latency impact)
- **`block` action**: Response buffered before delivery (adds latency but prevents delivery)

**Recommendation**: Use `flag` for monitoring, `block` only for critical patterns where you accept the latency cost.

---

## Rate Limiting / Firewall Rules

These rules protect against resource exhaustion and runaway agents.

```yaml
- name: "rate_limit_high"
  type: "requests_per_minute"
  threshold: 60
  severity: "critical"
  action: "block"
  description: "FIREWALL: Request rate exceeds 60/min - potential DoS or runaway agent"

- name: "rate_limit_warning"
  type: "requests_per_minute"
  threshold: 30
  severity: "warning"
  action: "flag"
  description: "FIREWALL: Elevated request rate (30/min)"

- name: "high_request_count"
  type: "request_count"
  threshold: 100
  severity: "warning"
  action: "flag"
  description: "FIREWALL: Session exceeded 100 requests"

- name: "very_high_request_count"
  type: "request_count"
  threshold: 500
  severity: "critical"
  action: "block"
  description: "FIREWALL: Session exceeded 500 requests - potential abuse"

- name: "long_running_session"
  type: "duration"
  threshold: 1800  # 30 minutes
  severity: "warning"
  action: "flag"
  description: "FIREWALL: Session running longer than 30 minutes"

- name: "excessive_session_duration"
  type: "duration"
  threshold: 3600  # 1 hour
  severity: "critical"
  action: "block"
  description: "FIREWALL: Session exceeded 1 hour - likely runaway agent"

- name: "large_response"
  type: "bytes_out"
  threshold: 10485760  # 10MB
  severity: "warning"
  action: "flag"
  description: "FIREWALL: Large data transfer (>10MB response)"

- name: "excessive_data_transfer"
  type: "bytes_total"
  threshold: 52428800  # 50MB
  severity: "critical"
  action: "block"
  description: "FIREWALL: Excessive data transfer (>50MB total)"
```

---

## OWASP LLM01 - Prompt Injection (REQUEST-SIDE)

Detects attempts to override system prompts or manipulate model behavior.

```yaml
- name: "prompt_injection_ignore"
  type: "content_match"
  target: "request"
  patterns:
    - "ignore\\s+(all\\s+)?(previous|prior|above)\\s+(instructions|prompts|rules)"
    - "disregard\\s+(all\\s+)?(previous|prior|system)\\s+(instructions|prompts)"
    - "forget\\s+(all\\s+)?(previous|prior|your)\\s+(instructions|training|rules)"
    - "override\\s+(system|previous|all)\\s+(prompt|instructions)"
  severity: "critical"
  action: "block"
  description: "LLM01: Prompt injection attempt - instruction override"

- name: "prompt_injection_jailbreak"
  type: "content_match"
  target: "request"
  patterns:
    - "you\\s+are\\s+now\\s+(DAN|a\\s+new|an?\\s+unrestricted)"
    - "pretend\\s+(you\\s+are|to\\s+be)\\s+(DAN|unrestricted|unfiltered)"
    - "act\\s+as\\s+(DAN|an?\\s+unrestricted|an?\\s+unfiltered)"
    - "enable\\s+(DAN|developer|jailbreak)\\s+mode"
    - "DAN\\s+mode\\s+(enabled|activated|on)"
    - "jailbreak(ed)?\\s+(mode|prompt|enabled)"
  severity: "critical"
  action: "terminate"
  description: "LLM01: Prompt injection attempt - jailbreak/DAN pattern"

- name: "prompt_injection_system"
  type: "content_match"
  target: "request"
  patterns:
    - "\\[system\\].*\\[/system\\]"
    - "<system>.*</system>"
    - "system\\s*:\\s*you\\s+are"
    - "new\\s+system\\s+prompt"
    - "inject.*system.*prompt"
  severity: "critical"
  action: "block"
  description: "LLM01: Prompt injection attempt - system prompt manipulation"

- name: "prompt_injection_delimiter"
  type: "content_match"
  target: "request"
  patterns:
    - "```\\s*(system|admin|root)"
    - "---+\\s*(new\\s+instructions|ignore\\s+above)"
    - "\\*\\*\\*\\s*(admin|system|override)"
  severity: "warning"
  action: "flag"
  description: "LLM01: Suspicious delimiter pattern in prompt"
```

---

## OWASP LLM02 - Insecure Output Handling (RESPONSE-SIDE)

Detects dangerous content in LLM responses that could be executed by clients.

```yaml
- name: "output_script_injection"
  type: "content_match"
  target: "response"
  patterns:
    - "<script[^>]*>"
    - "javascript:"
    - "on(click|load|error|mouseover)\\s*="
    - "eval\\s*\\("
    - "document\\.(write|cookie|location)"
  severity: "warning"
  action: "flag"
  description: "LLM02: Response contains potential XSS/script injection"

- name: "output_sql_content"
  type: "content_match"
  target: "response"
  patterns:
    - "(?i)(insert|update|delete|drop|alter|create)\\s+(into|from|table|database)"
    - "(?i)select\\s+.+\\s+from\\s+.+\\s+where"
    - "(?i);\\s*(drop|delete|truncate)\\s+"
  severity: "warning"
  action: "flag"
  description: "LLM02: Response contains SQL statements"

- name: "output_shell_commands"
  type: "content_match"
  target: "response"
  patterns:
    - "\\$\\s*\\(\\s*(curl|wget|bash|sh)\\s+"
    - "&&\\s*(rm|chmod|chown|sudo)\\s+"
    - "\\|\\s*(bash|sh|python|perl|ruby)\\s*$"
  severity: "warning"
  action: "flag"
  description: "LLM02: Response contains shell command patterns"

- name: "output_dangerous_code"
  type: "content_match"
  target: "response"
  patterns:
    - "pickle\\.loads"
    - "yaml\\.unsafe_load"
    - "eval\\s*\\(.*input"
    - "exec\\s*\\(.*input"
    - "__import__\\s*\\("
  severity: "critical"
  action: "flag"
  description: "LLM02: Response contains unsafe deserialization patterns"
```

---

## OWASP LLM06 - Sensitive Information Disclosure (BOTH)

Detects requests that may expose PII or sensitive data.

```yaml
- name: "pii_ssn_request"
  type: "content_match"
  target: "both"
  patterns:
    - "social\\s+security\\s+(number|#)"
    - "\\bssn\\b"
    - "\\d{3}-\\d{2}-\\d{4}"
  severity: "warning"
  action: "flag"
  description: "LLM06: SSN pattern detected"

- name: "pii_credit_card"
  type: "content_match"
  target: "both"
  patterns:
    - "credit\\s+card\\s+(number|#|info)"
    - "\\b(?:4[0-9]{12}(?:[0-9]{3})?|5[1-5][0-9]{14}|3[47][0-9]{13})\\b"
    - "\\bcvv\\b"
    - "\\bcvc\\b"
  severity: "warning"
  action: "flag"
  description: "LLM06: Credit card pattern detected"

- name: "credentials_request"
  type: "content_match"
  target: "request"
  patterns:
    - "(show|give|list|extract)\\s+(me\\s+)?(the\\s+)?api[_\\s]?key"
    - "(show|give|list|extract)\\s+(me\\s+)?(the\\s+)?password"
    - "(read|show|cat|display)\\s+(the\\s+)?\\.env\\s+file"
    - "(list|show|dump)\\s+(all\\s+)?credentials"
  severity: "warning"
  action: "flag"
  description: "LLM06: Credentials request"

- name: "pii_bulk_extraction"
  type: "content_match"
  target: "request"
  patterns:
    - "(list|show|give|extract)\\s+(all\\s+)?(user|customer|employee)\\s+(data|info|records)"
    - "dump\\s+(the\\s+)?(database|user\\s+table|customer\\s+data)"
  severity: "warning"
  action: "flag"
  description: "LLM06: Bulk data extraction request"
```

---

## OWASP LLM07 - Insecure Plugin Design (REQUEST-SIDE)

Monitors tool/function calling patterns for security issues.

```yaml
- name: "tool_code_execution"
  type: "content_match"
  target: "request"
  patterns:
    - "\"function\"\\s*:\\s*\"(run|execute|eval)_code\""
    - "\"name\"\\s*:\\s*\"(code_interpreter|execute_python|run_script)\""
    - "\"type\"\\s*:\\s*\"code_interpreter\""
  severity: "critical"
  action: "flag"
  description: "LLM07: Tool requests code execution"

- name: "tool_credential_access"
  type: "content_match"
  target: "request"
  patterns:
    - "\"function\"\\s*:\\s*\"(get|read|fetch)_(secret|credential|password|key)\""
    - "\"name\"\\s*:\\s*\"(vault_read|secret_manager|get_api_key)\""
  severity: "critical"
  action: "block"
  description: "LLM07: Tool requests credential access"

- name: "tool_file_access"
  type: "content_match"
  target: "request"
  patterns:
    - "\"function\"\\s*:\\s*\"(read|write|delete|create)_file\""
    - "\"name\"\\s*:\\s*\"file_(read|write|delete|access)\""
  severity: "warning"
  action: "flag"
  description: "LLM07: Tool requests file system access"

- name: "tool_network_access"
  type: "content_match"
  target: "request"
  patterns:
    - "\"function\"\\s*:\\s*\"(http_request|fetch|curl|wget)\""
    - "\"name\"\\s*:\\s*\"(web_request|api_call|http_get|http_post)\""
  severity: "warning"
  action: "flag"
  description: "LLM07: Tool requests network access"

- name: "tool_database_access"
  type: "content_match"
  target: "request"
  patterns:
    - "\"function\"\\s*:\\s*\"(query|sql|database)_\""
    - "\"name\"\\s*:\\s*\"(run_sql|db_query|execute_query)\""
  severity: "warning"
  action: "flag"
  description: "LLM07: Tool requests database access"
```

---

## OWASP LLM08 - Excessive Agency (REQUEST-SIDE)

Detects requests for dangerous system access.

```yaml
- name: "shell_execution"
  type: "content_match"
  target: "request"
  patterns:
    - "(run|execute)\\s+(a\\s+)?(bash|shell|terminal)\\s+(command|script)"
    - "bash\\s+-c\\s+"
    - "/bin/(ba)?sh\\s+"
  severity: "critical"
  action: "block"
  description: "LLM08: Shell execution request"

- name: "destructive_file_ops"
  type: "content_match"
  target: "request"
  patterns:
    - "rm\\s+(-rf?|--recursive)\\s+/"
    - "rm\\s+-rf\\s+\\*"
    - "(delete|remove|wipe)\\s+all\\s+(files|data|everything)"
  severity: "critical"
  action: "terminate"
  description: "LLM08: Destructive file operation"

- name: "privilege_escalation"
  type: "content_match"
  target: "request"
  patterns:
    - "sudo\\s+"
    - "(run|execute)\\s+(as|with)\\s+root"
    - "privilege\\s+(escalation|elevation)"
  severity: "critical"
  action: "block"
  description: "LLM08: Privilege escalation attempt"

- name: "network_exfiltration"
  type: "content_match"
  target: "request"
  patterns:
    - "curl.*\\|\\s*(ba)?sh"
    - "wget.*\\|\\s*(ba)?sh"
    - "reverse\\s+shell"
  severity: "critical"
  action: "terminate"
  description: "LLM08: Data exfiltration attempt"

- name: "sql_injection"
  type: "content_match"
  target: "request"
  patterns:
    - "drop\\s+(table|database)\\s+"
    - ";\\s*(drop|delete|truncate|update)\\s+"
    - "union\\s+select"
    - "'\\s*or\\s+'?1'?\\s*=\\s*'?1"
  severity: "critical"
  action: "terminate"
  description: "LLM08: SQL injection attempt"

- name: "network_scanning"
  type: "content_match"
  target: "request"
  patterns:
    - "nmap\\s+"
    - "port\\s+scan"
    - "(scan|enumerate)\\s+(the\\s+)?(network|ports|hosts)"
  severity: "warning"
  action: "flag"
  description: "LLM08: Network reconnaissance"
```

---

## OWASP LLM09 - Overreliance Mitigation

Decision audit logging and confidence tracking.

```yaml
- name: "high_stakes_medical"
  type: "content_match"
  target: "both"
  patterns:
    - "(diagnose|diagnosis|treatment|prognosis)\\s+(for|of)"
    - "should\\s+I\\s+(take|stop|start)\\s+.*(medication|medicine|drug)"
    - "(medical|health)\\s+(advice|recommendation|decision)"
  severity: "warning"
  action: "flag"
  description: "LLM09: High-stakes medical domain - requires human verification"

- name: "high_stakes_legal"
  type: "content_match"
  target: "both"
  patterns:
    - "(legal|law)\\s+(advice|recommendation|opinion)"
    - "(sue|lawsuit|litigation|liability)\\s+"
    - "is\\s+(this|it)\\s+(legal|illegal|lawful|unlawful)"
  severity: "warning"
  action: "flag"
  description: "LLM09: High-stakes legal domain - requires human verification"

- name: "high_stakes_financial"
  type: "content_match"
  target: "both"
  patterns:
    - "(invest|investment|portfolio)\\s+(advice|recommendation|decision)"
    - "should\\s+I\\s+(buy|sell|hold|invest)\\s+"
    - "(financial|money|investment)\\s+(advice|decision|recommendation)"
  severity: "warning"
  action: "flag"
  description: "LLM09: High-stakes financial domain - requires human verification"

- name: "low_confidence_hedging"
  type: "content_match"
  target: "response"
  patterns:
    - "I('m|\\s+am)\\s+not\\s+(sure|certain|confident)"
    - "I\\s+(think|believe|guess|suppose)\\s+(that\\s+)?(it|this)\\s+(might|may|could)"
    - "(please|you\\s+should)\\s+(verify|confirm|check|consult)"
  severity: "info"
  action: "flag"
  description: "LLM09: Low-confidence response detected - verify before acting"
```

---

## OWASP LLM10 - Model Theft (REQUEST-SIDE)

Detects attempts to extract model weights, architecture, or training data.

```yaml
- name: "model_extraction"
  type: "content_match"
  target: "request"
  patterns:
    - "(extract|dump|export)\\s+(the\\s+)?(model|weights|parameters)"
    - "(what|describe)\\s+(is|are)\\s+your\\s+(weights|parameters|architecture)"
  severity: "warning"
  action: "flag"
  description: "LLM10: Model extraction attempt"

- name: "training_data_extraction"
  type: "content_match"
  target: "request"
  patterns:
    - "(what|which)\\s+(data|dataset|examples)\\s+(were|was)\\s+(you|the\\s+model)\\s+trained\\s+on"
    - "(show|give|list)\\s+me\\s+(examples|samples)\\s+(from|of)\\s+(your|the)\\s+training"
    - "repeat\\s+(exactly|verbatim|word\\s+for\\s+word)"
  severity: "warning"
  action: "flag"
  description: "LLM10: Training data extraction attempt"

- name: "model_replication"
  type: "content_match"
  target: "request"
  patterns:
    - "(create|build|train|replicate)\\s+(a\\s+)?(copy|clone|replica)\\s+of\\s+(you|this\\s+model)"
    - "(distill|compress|extract)\\s+(your|the\\s+model's?)\\s+(knowledge|capabilities)"
    - "knowledge\\s+distillation"
  severity: "warning"
  action: "flag"
  description: "LLM10: Model replication/distillation attempt"
```

---

## NIST AI RMF - Anomaly Detection

General anomaly and abuse detection patterns.

```yaml
- name: "automated_abuse_pattern"
  type: "content_match"
  patterns:
    - "\\{\\{.*\\}\\}"
    - "\\$\\{.*\\}"
    - "<%.*%>"
  severity: "warning"
  action: "flag"
  description: "NIST: Template/variable injection pattern detected"

- name: "encoding_evasion"
  type: "content_match"
  patterns:
    - "base64\\s+(decode|encode)"
    - "\\\\x[0-9a-fA-F]{2}"
    - "\\\\u[0-9a-fA-F]{4}"
  severity: "warning"
  action: "flag"
  description: "NIST: Encoding/obfuscation pattern detected"

- name: "resource_exhaustion"
  type: "content_match"
  patterns:
    - "(generate|create|write)\\s+(a\\s+)?(very\\s+)?(long|huge|massive|infinite)"
    - "repeat\\s+(this|the\\s+following)\\s+(forever|infinitely|1000000)"
    - "loop\\s+(forever|infinitely)"
  severity: "warning"
  action: "flag"
  description: "LLM04: Potential resource exhaustion attempt"
```

---

## Creating Custom Rules

You can add your own rules by defining them in the `rules` section of your config:

```yaml
policy:
  enabled: true
  preset: standard  # Start with standard rules

  rules:
    # Custom rule example
    - name: "block_competitor_mentions"
      type: "content_match"
      target: "request"
      patterns:
        - "use\\s+(competitor|rival)\\s+instead"
      severity: "warning"
      action: "flag"
      description: "Custom: Competitor mention detected"
```

Custom rules are **appended** to preset rules, so you get both.
