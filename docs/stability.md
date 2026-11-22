# Stability and Compatibility

This page says how much to rely on each part of ELIDA staying the same between
releases. ELIDA is versioned with [Semantic Versioning](https://semver.org/) and
is still below version 1, so any release may change any surface; every breaking
change is listed in the changelog under **Breaking**, with the migration. The
levels below describe intent, not a guarantee, and may be tightened later.

- **Stable:** changes are avoided. A breaking change is called out in the
  changelog and, where ELIDA can detect it, warned about at startup before it
  takes effect.
- **Experimental:** works and is tested, but may change or be removed in any
  release. Changes are still listed in the changelog.
- **Internal:** not covered.

## Surfaces

| Surface | Level | What is covered |
|---------|-------|-----------------|
| Config file keys and defaults | Stable | Existing YAML keys keep their name, type and meaning where possible. A new key defaults to the old behavior. |
| Environment variable overrides | Stable | Existing `ELIDA_*` variables keep working. |
| Proxy behavior | Stable | Session identity precedence (header, body path, `user` field, client-IP fallback), failover, kill and kill-block modes. |
| Control API (`/control/*`) | Stable | Existing endpoints, methods and response fields. New fields may appear; clients must ignore fields they do not know. |
| Session detail records | Stable | Existing fields of the stored and exported record. New fields may appear. |
| Policy rule names and categories | Stable | Existing built-in rule names and their OWASP categories. A rule's patterns may be tightened or loosened in a minor release to fix false positives and misses. |
| Helm chart values | Stable | Existing values keep their name and meaning. |
| Dashboard | Not covered | The UI may change in any release. Use the control API for automation. |
| Semantic injection detection | Experimental | Config keys under `decision.*`, the thresholds, the model artifact and its scores. See [semantic-detection.md](semantic-detection.md). |
| Temporal correlation rules | Experimental | Shadow and policy correlation shapes. |
| Instruction integrity and provenance | Experimental | Any rule or setting that reads instruction channels. |
| Panel and M3-lite scoring | Experimental | `/control/panel` and the scoring behind it. |
| Go packages under `internal/` | Internal | Not importable by other modules and not covered. |
| Metric and telemetry names | Stable once named in docs | Names documented in [semantic-detection.md](semantic-detection.md) or [integrations.md](integrations.md) are covered; others are not. |

## Deprecation

A stable surface is deprecated, with a changelog entry, before it is removed.
Where ELIDA can detect use of a deprecated surface it logs at startup.

## What the proxy does not promise

- A classification, score or rule match is a probabilistic or pattern-based
  signal, not a guarantee. Shadow and audit modes record evidence and protect
  nothing.
- Content captured by an ELIDA release can only be read back by the same
  or a later release. The on-disk SQLite schema is migrated forward, not backward.
- Session identity derived from a request body or header is client-controlled.
  See the security note in [configuration.md](configuration.md).
