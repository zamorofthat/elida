# Stability and Compatibility

This page says what ELIDA promises not to break once it reaches 1.0, and what it
does not. Until 1.0, treat every surface below as subject to change in any minor
release; the changelog calls out breaking changes.

> **Status: proposed.** The classification below is a proposal for the 1.0
> release. It becomes policy when it ships with 1.0.

ELIDA follows [Semantic Versioning](https://semver.org/). From 1.0:

- **Patch** releases fix bugs and security issues and never change a stable surface.
- **Minor** releases may add to any surface. They never remove or change the
  meaning of anything marked *stable*.
- **Major** releases may break stable surfaces. Each break is listed in the
  changelog under **Breaking**, with the migration.

## Surfaces

| Surface | Level | What is covered |
|---------|-------|-----------------|
| Config file keys and defaults | Stable | Existing YAML keys keep their name, type and meaning. A new key defaults to the old behavior. |
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

**Experimental** means the surface works and is tested, but it may change or be
removed in a minor release. Experimental changes are still listed in the
changelog.

## Deprecation

A stable surface is deprecated for at least one minor release before it is
removed in the next major release. The deprecation is announced in the
changelog and, where ELIDA can detect it, logged at startup.

## What the proxy does not promise

- A classification, score or rule match is a probabilistic or pattern-based
  signal, not a guarantee. Shadow and audit modes record evidence and protect
  nothing.
- Content captured by an ELIDA release can only be read back by the same
  or a later release. The on-disk SQLite schema is migrated forward, not backward.
- Session identity derived from a request body or header is client-controlled.
  See the security note in [configuration.md](configuration.md).
