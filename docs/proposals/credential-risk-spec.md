# Spec (for later): risk that accumulates on the client, not just the session

**Status:** specification, not implemented. Part A (per-client proxy keys,
`proxy.auth.keys`) ships first. This is the follow-up that fixes what Part A
does not: a keyed caller resetting its own risk by changing its session ID.
Read [session-identity-credentials.md](session-identity-credentials.md) for the
problem and Part A.

## Goal

A keyed client that sends a new session ID on every request must not be able to
start from zero each time. Cumulative risk, and therefore the risk ladder and
slow-burn detection, must follow the client across its sessions.

## Non-goals

- Callers on the shared key or a trusted network. They have no client name, so
  nothing here applies to them.
- Deciding who a *user* behind a client is. A gateway that serves many end users
  with one key is one client; see "The gateway problem".
- Changing how any single violation is scored, or any rule.

## Where risk lives today

- Risk is kept per session ID in the policy engine as a `FlaggedSession`: a list
  of `ViolationEvent`s (rule, severity, source role, timestamp), not a running
  total.
- The score is recomputed from those events with exponential decay,
  `weight(severity, source role) * e^(-lambda * seconds)`, summed and capped at
  `MaxRiskScore` (100). `DefaultDecayLambda` is 0.002, a half-life of about 5.8
  minutes, so one false positive is mostly gone after half an hour.
- `risk_ladder.thresholds` map a score to an action: observe, warn, throttle,
  block, terminate. `determineRiskAction` turns the score into the action, and
  `ShouldBlockByRisk` and `ShouldTerminateByRisk` are what the proxy consults.
- Every violation enters through `recordViolations(sessionID, ...)`, and
  `AddExternalRiskPoints` adds points from outside the rule engine. The engine
  does not know which client a session belongs to.

## Design

**A second record, keyed on the client.** Alongside the session's
`FlaggedSession`, keep one per client, under the key `client:<name>`. Every
event recorded for a keyed session is recorded on both. Sessions from callers
with no client name record only on the session, as today.

**The ladder reads the larger score.** The action for a request is the one for
`max(sessionScore, clientScore)`. A client that rotates session IDs then meets
the same ladder its history earns, while a well-behaved client's score is no
different from what its session alone would give.

**Same decay and cap.** The client record uses the same `calculateRiskScore`:
same lambda, same weights, same cap, same threshold table. No new tuning surface
in the first version.

**The seam.** The engine needs the client name at the point it records. Pass it
from the proxy, which already knows it (`authClient`), as an optional field on
the record call rather than deriving it from the session ID string. Parsing
`name:id` back out of the ID is fragile and must not be the mechanism.

**Visibility first.** Expose the client record at
`GET /control/clients` and `GET /control/clients/{name}`: score, action, event
count, the sessions it spans. Add the client's score to the session detail. An
operator should be able to see a client's score before the ladder acts on it.

## The gateway problem

A gateway or an agent platform that fronts many end users with one key is one
client. Pooling risk on that key means one bad end user raises the score for
every user behind it, and a credential-level block would take them all offline.
This is the main risk of the whole feature.

Mitigations, in the order I would apply them:

1. **First version acts on the client score only up to throttle.** A client-level
   score can warn and throttle. It cannot block or terminate. Block and
   terminate stay session-level, driven by the session's own score. That bounds
   the damage of pooling to a slowdown.
2. **A per-client switch**, `client_risk: false` on a key, for gateway keys,
   leaving the client record visible but never fed to the ladder.
3. **Pooling is visible.** The client view shows how many distinct sessions feed
   the score, so a gateway key with hundreds of sessions is recognizable.

## Config

```yaml
proxy:
  auth:
    keys:
      - name: team-a
        key: ${TEAM_A_KEY}
        client_risk: true        # default true; false for a shared gateway key
risk_ladder:
  client:
    max_action: throttle         # highest action a client score may cause
```

`risk_ladder.client.max_action` defaults to `throttle`. Raising it to `block` is
an explicit decision, documented as the setting that lets one session take a
client offline.

## Bounds

Clients are named by the operator and few, so the number of client records is
bounded by the config. Each record's event list needs the same bound the
session's has, and the same eviction rule, so one noisy client cannot grow it
without limit. A client removed from the config has its record dropped at the
next reload.

## Restart and Redis

Client records are in memory like session risk today. Decide, and state, whether
they survive a restart or a failover. The honest default is that they do not,
matching session risk, with the limit written in the docs.

## Tests to write first

- A keyed client that changes its session ID on every request reaches the
  throttle threshold; the same behavior with Part A alone does not.
- A client with `client_risk: false` never has its client score applied.
- The client action never exceeds `risk_ladder.client.max_action`, whatever the
  score.
- Two clients with the same violation history have independent scores.
- A caller with no client name records no client score.
- Decay: a client's score falls on the same curve as a session's.
- The client score is the same whether its events arrive in one session or fifty.
- Removing a client from the config drops its record.

## Open decisions

1. Is `throttle` the right ceiling for the first version, or should the first
   version only record and display the client score and not act on it at all?
2. Should client risk survive a restart?
3. Should a client-level kill exist at all, or stay a per-session control for
   good?
4. Is a per-key `client_risk` switch enough for gateways, or does a gateway need
   a way to supply a trustworthy end-user identity, which is a different feature?
