# Behavioral Panel

The behavioral panel is a small ensemble of session-level detectors. Each
member judges a finished session and reports an anomaly score on a common
0..1 scale. The panel combines the scores into one risk value that feeds the
existing risk ladder. It informs the policy engine and never enforces by
itself.

Shipped posture:

| Member | Seat | What it scores |
|--------|------|----------------|
| `m3-lite` | live, weight 1.0 | The behavioral fingerprint (see `fingerprint.*`): Mahalanobis distance over structural session features, bucketed. Its seat is fixed in code. |
| `tool-chain` | **shadow** by default, and only seated when an artifact is configured | How improbable the session's ordered tool-call sequence is under a per-class Markov chain, as a calibrated percentile. |

M3-lite is the only member that affects risk points or findings. The
tool-chain member is observed, persisted and counted, but a shadow member
never changes the panel's `RiskScore`, class, risk points or OCSF findings.
Nothing in the shipped configuration promotes it. See [the promotion
gate](#the-promotion-gate).

The panel exists only when behavioral fingerprinting is running
(`fingerprint.enabled: true` with `storage.enabled: true`). It is evaluated
once per session, when the session ends.

## The anomaly scale and the weight

Every member reports `anomaly` in `[0, 1]`. The panel clamps out-of-range
values (NaN becomes 0) and logs one warning per member name.

- **M3-lite** maps its fingerprint bucket to the points it has always added,
  divided by 20: `normal` 0, `minor` 0.10, `notable` 0.25, `anomalous` 0.50,
  `severe` 1.00. While `fingerprint.shadow` is `true` it still reports a real
  bucket and distance, but its anomaly stays 0. During warm-up it reports 0
  and no detail.
- **tool-chain** reports the calibrated percentile of the session's
  perplexity (0..1). A session with fewer than two tool calls reports
  anomaly 0 with `reason: insufficient-trajectory`. A session whose class has
  no chain in the artifact, and no `global` chain to fall back to, reports
  anomaly 0 with `reason: no-chain`.

The panel's `RiskScore` is the largest `weight * anomaly` among the live
(non-shadow) members, so members do not add up. A member that sets a veto
floors `RiskScore` to 1.0 whatever the weights; no shipped member emits a
veto. The member that set the score is the `driver` (`veto:<member>` for a
veto, empty when the score is 0).

Risk points are `round(RiskScore * 20)`, with 20 the points of a severe
fingerprint. The driver is the source label on those points. Because the
weight multiplies the anomaly, it is a dial on how many points a member can
add:

| Member | Weight | Anomaly | Points |
|--------|--------|---------|--------|
| tool-chain | 1.0 | 0.80 | 16 |
| tool-chain | 0.5 | 0.80 | 8 |
| tool-chain | 0.5 | 1.00 | 10 (the most a weight of 0.5 can add) |

M3-lite's points are unchanged by this scale: a notable bucket is still 5
points, anomalous 10 and severe 20.

## Configuration

The `panel` block is read once at startup; changing it needs a restart.

```yaml
panel:
  tool_chain_artifact: ""    # path to a tool-chain artifact JSON; empty = member not seated
  tool_chain_shadow: true    # true = observe only; false = counts toward the verdict
  tool_chain_weight: 1.0     # [0, 1]; only matters when tool_chain_shadow is false
```

| Key | Default | Meaning |
|-----|---------|---------|
| `panel.tool_chain_artifact` | `""` | Path to the tool-chain artifact. Empty means the member is not seated and the panel behaves as M3-lite alone. A missing or invalid artifact logs `tool-chain artifact not loaded; member not seated` and startup continues. |
| `panel.tool_chain_shadow` | `true` | `true` seats the member to be assessed and reported only. `false` lets it influence the verdict at `tool_chain_weight`. |
| `panel.tool_chain_weight` | `1.0` | The member's weight, validated to `[0, 1]`; a value outside that range fails config validation. It has no effect while `tool_chain_shadow` is `true`. |

The artifact is a JSON file with `schema_version: "1"`, `member_type:
"tool-chain"`, a `generated_by` string (reported as the member's version) and
a `classes` map. Each class holds a Markov chain (`states`, `start`,
`matrix`, `oov_floor`) and a perplexity-to-percentile `calibration` table. A
session is scored against the chain for its class, or the `global` chain when
its class has none. Tool names outside a chain's states take its `oov_floor`
probability.

When the member is seated, startup logs `panel: tool-chain member seated`
with `version`, `shadow` and `weight`.

## Observing the panel

Three surfaces, none of which carries request content:

**`panel_opinions` on the session record.** One entry per seated member, in
seat order, stored with every ended session (including sessions that were
still in M3-lite warm-up) and returned by `GET /control/history/{id}`. The
history list leaves it out. Each entry has `member`, `shadow`, `weight`,
`anomaly`, and, when set, `veto`, `bucket`, `distance` (M3-lite),
`perplexity`, `percentile`, `class_used` and `reason` (tool-chain). Fixed
fields only: numbers, enums and short tokens. See
[Session Records](session-records.md#panel-opinions).

**The `fingerprint score` log line.** One line per scored session (none
during warm-up), with `session_id`, `class`, `distance`, `bucket`, `shadow`
(M3-lite's), `panel_driver`, `panel_risk` (the 0..1 `RiskScore`) and
`members`, the same list as `panel_opinions`.

**`GET /control/panel`.** The seated roster with each member's rolling
stats: `sessions_scored`, `mean_anomaly`, `p95_anomaly` and `veto_count`.
These are in memory and reset on restart; the mean and p95 cover the last
1000 scores per seat. See [API Reference](api.md#behavioral-panel).

Use the three together when judging a shadow member: `panel_opinions` gives
the per-session evidence, the log line shows what the panel would have done
with it, and `/control/panel` gives the live distribution.

## Live members and OCSF findings

Each live member whose `weight * anomaly` reaches 0.25 (the notable rung)
emits its own OCSF 2004 Detection Finding when OCSF export is enabled. For
M3-lite that is the notable, anomalous and severe buckets, exactly as before.
Shadow members never emit one.

A live tool-chain finding has analytic `tool-chain Markov`, severity from the
member's percentile (at least 0.95 critical, at least 0.75 warning, otherwise
informational), and an `elida.tool_chain` object with `perplexity`,
`percentile` and `class_used`. No tool names or arguments are included.

## `fingerprint.shadow` caveat

`fingerprint.shadow` defaults to `true`: M3-lite scores and persists but
never adds points. The session scoring path returns before adding risk
points or emitting findings when M3-lite is in shadow, and that return
applies to the whole panel. A live tool-chain member therefore adds no
points and emits no finding while `fingerprint.shadow` is `true`. To make a
promoted tool-chain member count, `fingerprint.shadow` must also be `false`.
Opinions are still persisted and the `fingerprint score` line is still
logged in that state.

## The promotion gate

The tool-chain member ships in shadow and should stay there until its
shadow evidence clears all of the following, measured from `panel_opinions`:

- At least 500 shadow-scored sessions with at least two tool calls.
- An AUC of at least 0.85.
- A false-positive rate of at most 2% at `weight * anomaly >= 0.25`.
- A p95 anomaly that moves by no more than 0.05 between artifacts built from
  two disjoint halves of the data.

First flip: set `tool_chain_shadow: false` with `tool_chain_weight` at 0.5
or lower (and `fingerprint.shadow: false`, see above), restart, and observe
for a window. A weight of 0.5 caps the member at 10 points. Raise the weight
only after that window.

**Roll back:** set `panel.tool_chain_shadow: true` and restart. The member
goes back to observe-only and its points and findings stop; its opinions
keep being recorded.

### Before promoting tool-chain (2b-ii)

Do not flip the tool-chain member live yet. The session scoring path returns
early when M3-lite has no detail, which is the case during M3-lite warm-up
(and, as above, when M3-lite is in shadow). That return suppresses risk
points and OCSF findings for **all** members, including a live tool-chain.
Until that path is changed, a live tool-chain member would add nothing for
any session scored during M3-lite warm-up. Revisit it before the first live
flip.

Also not implemented yet:

- The dashboard does not display the panel driver or the tool-chain
  percentile.
- `export-sessions` does not include `panel_opinions`.
