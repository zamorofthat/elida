# Security Policy

ELIDA takes security seriously. As a security-focused proxy for AI model traffic, we hold ourselves to high standards for vulnerability management and responsible disclosure.

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.1.x   | :white_check_mark: |

## Reporting a Vulnerability

We appreciate responsible disclosure of security vulnerabilities. Please **do not** report security vulnerabilities through public GitHub issues.

### How to Report

**Email**: security@elida.dev (or create a private security advisory on GitHub)

**GitHub Security Advisory**:
1. Go to the [Security tab](https://github.com/zamorofthat/elida/security)
2. Click "Report a vulnerability"
3. Fill out the private security advisory form

### What to Include

Please include as much of the following information as possible:

- **Type of vulnerability** (e.g., injection, authentication bypass, information disclosure)
- **Affected component** (e.g., proxy handler, policy engine, WebSocket handler)
- **Step-by-step reproduction instructions**
- **Proof-of-concept or exploit code** (if available)
- **Impact assessment** - what an attacker could achieve
- **Suggested fix** (if you have one)

### What to Expect

| Timeframe | Action |
|-----------|--------|
| 24 hours | Acknowledgment of your report |
| 72 hours | Initial assessment and severity rating |
| 7 days | Detailed response with remediation plan |
| 90 days | Public disclosure (coordinated with reporter) |

We follow a **90-day disclosure timeline** unless:
- The vulnerability is already being actively exploited
- An extension is mutually agreed upon
- The fix requires coordination with upstream dependencies

### Safe Harbor

We consider security research conducted in accordance with this policy to be:
- Authorized under the Computer Fraud and Abuse Act (CFAA)
- Exempt from DMCA restrictions on circumvention
- Lawful and conducted in good faith

We will not pursue legal action against researchers who:
- Act in good faith to avoid privacy violations and data destruction
- Do not exploit vulnerabilities beyond proof-of-concept
- Report findings promptly and allow reasonable time for remediation
- Do not publicly disclose before coordinated disclosure timeline

## Security Best Practices for ELIDA Deployments

### Production Deployment Checklist

- [ ] **Enable TLS** - Always use HTTPS/WSS in production
  ```yaml
  tls:
    enabled: true
    cert_file: "/path/to/cert.pem"
    key_file: "/path/to/key.pem"
  ```

- [ ] **Enable Control API Authentication**
  ```yaml
  control:
    auth:
      enabled: true
      api_key: "${ELIDA_CONTROL_API_KEY}"  # Use environment variable
  ```

- [ ] **Use Policy Enforcement Mode** (not audit) for production
  ```yaml
  policy:
    enabled: true
    mode: enforce  # Not "audit"
  ```

- [ ] **Secure Redis** (if using Redis session store)
  - Use authentication (`requirepass`)
  - Enable TLS for Redis connections
  - Restrict network access

- [ ] **Protect SQLite Database**
  - Restrict file permissions (`chmod 600 data/elida.db`)
  - Regular backups of flagged session data

- [ ] **Network Segmentation**
  - Control API (`:9090`) should not be exposed to the internet
  - Use internal load balancer or VPN for admin access

### Environment Variables with Secrets

Never commit secrets to source control. Use environment variables:

```bash
# Required for production
export ELIDA_CONTROL_API_KEY="$(openssl rand -base64 32)"

# If using Redis
export ELIDA_REDIS_PASSWORD="your-redis-password"

# If using custom TLS
export ELIDA_TLS_CERT_FILE="/path/to/cert.pem"
export ELIDA_TLS_KEY_FILE="/path/to/key.pem"
```

## OpenSSF Best Practices

This project follows [OpenSSF Best Practices](https://bestpractices.coreinfrastructure.org/) guidelines:

### Security Tooling

| Tool | Purpose | Integration |
|------|---------|-------------|
| **[Aikido Security](https://www.aikido.dev)** | Continuous security monitoring | External |
| **gosec** | Go security linter | CI |
| **govulncheck** | Go vulnerability scanner | CI |
| **Semgrep** | SAST for security patterns | CI |
| **TruffleHog** | Secret detection in git history | CI |
| **golangci-lint** | Code quality and security | CI |

#### External Security Monitoring

We use **Aikido Security** for continuous vulnerability monitoring, providing:
- Automated vulnerability scanning
- Dependency risk analysis
- Real-time security alerts

### Dependency Management

- Dependencies are minimal by design
- `go.sum` provides cryptographic verification
- Dependabot enabled for automated security updates
- No CGO dependencies (pure Go for reproducible builds)

### Secure Development

- All changes require code review
- CI must pass before merge (lint, security scan, tests)
- Signed commits encouraged
- Branch protection on `main`

## Security-Related Configuration

### Policy Presets

ELIDA includes security policy presets aligned with OWASP LLM Top 10:

| Preset | Use Case | Coverage |
|--------|----------|----------|
| `minimal` | Development | Basic rate limits |
| `standard` | Production | OWASP basics + rate limits |
| `strict` | High-security | Full OWASP + NIST + PII |

```yaml
policy:
  enabled: true
  preset: strict  # Recommended for production
  mode: enforce
```

### Capture Mode Considerations

- `flagged_only` (default): Only captures policy-violating requests
- `all`: Captures everything - ensure compliance with data retention policies

```yaml
storage:
  capture_mode: "flagged_only"  # Recommended default
  max_capture_size: 10000       # Limit body capture size
```

## Known Security Considerations

### By Design

1. **Request/Response Logging**: ELIDA can capture full request/response bodies for security forensics. Ensure compliance with your organization's data handling policies.

2. **API Key Pass-through**: ELIDA forwards API keys to backend providers. It does not store or log API keys.

3. **Session Identification**: Session IDs are derived from client IP + backend. This is intentional for tracking but consider privacy implications.

### Mitigations

1. **Prompt Injection**: Policy engine detects common patterns, but is not a complete defense. Consider defense-in-depth with application-level validation.

2. **Regex DoS (ReDoS)**: Policy regex patterns are pre-compiled and tested, but custom rules should be reviewed for catastrophic backtracking.

## Acknowledgments

We thank the security researchers who have helped improve ELIDA's security:

*No vulnerabilities have been reported yet. Be the first!*

---

## Contact

- **Security Issues**: security@elida.dev or GitHub Security Advisory
- **General Questions**: [GitHub Discussions](https://github.com/zamorofthat/elida/discussions)
- **Bug Reports**: [GitHub Issues](https://github.com/zamorofthat/elida/issues)

---

*This security policy follows [OpenSSF Security Policies](https://github.com/ossf/oss-vulnerability-guide) and [GitHub's recommended format](https://docs.github.com/en/code-security/getting-started/adding-a-security-policy-to-your-repository).*
