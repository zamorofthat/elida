# ELIDA Memory Optimization Guide

**Target:** 10-15KB per session (10K sessions = 100-150MB)

---

## Current State (v0.1)

Measured: **~48KB per session** with policy enabled

### Memory Breakdown

| Component | Current | Notes |
|-----------|---------|-------|
| Session struct base | ~500 bytes | ID, timestamps, counters |
| `sync.RWMutex` | 24 bytes | Required for concurrency |
| `Metadata` map | ~48 bytes | Allocated even if empty |
| `BackendsUsed` map | ~48 bytes | Allocated even if empty |
| `RequestTimes` slice | ~3KB | 120 entries × 24 bytes (2 min window) |
| `killChan` channel | ~96 bytes | Required for kill signaling |
| Strings (ID, backend, addr) | ~200 bytes | Variable |
| **Subtotal (Session)** | **~4KB** | |
| | | |
| **Policy Capture** | | |
| `CapturedRequest` struct | ~100 bytes | Per captured request |
| `RequestBody` string | up to 10KB | `max_capture_size` setting |
| `ResponseBody` string | up to 10KB | `max_capture_size` setting |
| Multiple captures | ×N | Unbounded currently |
| **Subtotal (Capture)** | **~20-40KB** | Main memory consumer |

---

## Optimization Plan

### Phase 1: Session Struct (~4KB → ~1.5KB)

| Change | Savings | Complexity |
|--------|---------|------------|
| Circular buffer for `RequestTimes` | ~2KB | Low |
| Lazy map initialization | ~100 bytes | Low |
| Shorter session ID format | ~50 bytes | Low |

**Circular Buffer Implementation:**
```go
// Before: unbounded slice
RequestTimes []time.Time  // Can grow to 3KB+

// After: fixed-size circular buffer
const MaxRequestTimes = 128
requestTimes [MaxRequestTimes]time.Time  // Fixed 3KB, but predictable
rtHead, rtCount int                       // 16 bytes overhead
```

**Lazy Maps:**
```go
// Before: allocated on creation
Metadata:     make(map[string]string),
BackendsUsed: make(map[string]int),

// After: nil until first use
func (s *Session) SetMetadata(k, v string) {
    if s.Metadata == nil {
        s.Metadata = make(map[string]string)
    }
    s.Metadata[k] = v
}
```

### Phase 2: Capture Optimization (~40KB → ~8KB)

| Change | Savings | Trade-off |
|--------|---------|-----------|
| Reduce `max_capture_size` to 2KB | ~16KB | Less context for forensics |
| Limit captures per session to 3 | ~30KB | Miss repeated violations |
| Capture only trigger context (±500 bytes around match) | ~18KB | Partial content |
| Stream captures to storage | ~40KB | Requires storage enabled |

**Recommended defaults:**
```yaml
policy:
  max_capture_size: 2000      # 2KB (down from 10KB)
  max_captures_per_session: 3 # Limit captures
  capture_context_bytes: 500  # ±500 bytes around match
```

**Stream-to-Storage Mode (best option):**
```go
// Instead of holding in memory:
type FlaggedSession struct {
    CapturedContent []CapturedRequest  // In memory - expensive
}

// Stream directly to SQLite/telemetry:
type FlaggedSession struct {
    CaptureCount int  // Just track count
    // Content written to storage immediately
}
```

### Phase 3: Future Feature Budget

Reserve headroom for planned features:

| Feature | Estimated Memory | Notes |
|---------|-----------------|-------|
| LLM-as-judge results | ~500 bytes | Classification + confidence |
| Token counting | ~100 bytes | Input/output token counts |
| Cost tracking | ~50 bytes | Per-backend costs |
| Latency percentiles | ~200 bytes | P50/P95/P99 tracking |
| **Reserved** | **~1KB** | |

---

## Target Memory Budget

| Component | Target | Notes |
|-----------|--------|-------|
| Session struct | 1.5KB | Optimized |
| Policy state | 500 bytes | Violation list only |
| Capture metadata | 500 bytes | Count + refs to storage |
| Future features | 1KB | Reserved |
| **Total** | **~3.5KB** | Without content capture |
| | | |
| With inline capture (3 × 2KB) | +6KB | If storage disabled |
| **Total (no storage)** | **~10KB** | Meets target |

---

## Configuration Recommendations

### High-Density Mode (10K+ sessions)
```yaml
policy:
  enabled: true
  capture_flagged: true
  max_capture_size: 2000        # 2KB
  max_captures_per_session: 3

storage:
  enabled: true                 # Stream captures to disk
  capture_to_storage: true      # Don't hold in memory
```

### Standard Mode (1K-10K sessions)
```yaml
policy:
  enabled: true
  capture_flagged: true
  max_capture_size: 5000        # 5KB
  max_captures_per_session: 5
```

### Debug/Forensics Mode (<1K sessions)
```yaml
policy:
  enabled: true
  capture_flagged: true
  max_capture_size: 50000       # 50KB
  max_captures_per_session: 20
```

---

## Implementation Priority

1. **[High] Stream captures to storage** - Biggest win, ~40KB savings
2. **[Medium] Add `max_captures_per_session` limit** - Easy, ~20KB savings
3. **[Low] Circular buffer for RequestTimes** - Predictable memory
4. **[Low] Lazy map initialization** - Minor savings

---

## Future: VectorScan Integration

[VectorScan](https://github.com/VectorCamp/vectorscan) (Hyperscan fork) is the industry standard for high-performance pattern matching used by:
- Snort/Suricata (IDS/IPS)
- ModSecurity (WAF)
- DPI engines

**Current ELIDA approach:**
```go
// Sequential regex evaluation - O(patterns × content)
for _, rule := range rules {
    for _, pattern := range rule.CompiledPatterns {
        if pattern.MatchString(content) { ... }
    }
}
```

**VectorScan approach:**
```go
// Compile all patterns once at startup
db := vectorscan.Compile(allPatterns)

// Single-pass matching - O(content)
db.Scan(content, func(id uint, from, to uint64) {
    // Pattern 'id' matched at position from:to
})
```

**Benefits:**
| Metric | Current (regexp) | VectorScan |
|--------|-----------------|------------|
| Pattern count | ~50 rules | 10,000+ rules |
| Scan complexity | O(P × N) | O(N) |
| Memory (compiled) | ~1MB | ~5-10MB (shared) |
| Streaming support | Manual overlap | Native |

**Go bindings:** [flier/gohs](https://github.com/flier/gohs) (Hyperscan-compatible)

**Trade-off:** Adds CGO dependency, larger binary, but significant CPU reduction for high rule counts.

**When to implement:** When rule count exceeds ~100 or CPU becomes bottleneck.

---

## Benchmarking

Run memory benchmark:
```bash
./scripts/benchmark.sh --memory
```

Profile with pprof:
```bash
go tool pprof http://localhost:9090/debug/pprof/heap
```

Check per-session memory:
```bash
curl -s localhost:9090/control/stats | jq '.memory_per_session_kb'
```

---

## Industry Comparison

| System | Memory/Session | Notes |
|--------|---------------|-------|
| HAProxy stick table | ~74 bytes | Tracking only, no content |
| NGINX rate limit | ~128 bytes | Tracking only |
| ModSecurity | ~128KB | Request body buffering |
| **ELIDA (target)** | **~10KB** | Tracking + limited capture |
| **ELIDA (current)** | ~48KB | Tracking + full capture |

ELIDA's target of 10KB is reasonable given we provide:
- Full session lifecycle tracking (vs just rate limiting)
- Content inspection and capture (vs no capture)
- Policy violation forensics (unique to ELIDA)

---

*Last updated: January 2026*
