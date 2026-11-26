# Proposal: per-client credentials as the anchor for session identity

**Status:** proposal for review. Nothing here is implemented. It is a design to
accept, change or reject; the decisions it needs are listed at the end.

## The problem

Every stateful control hangs off the session ID: cumulative risk, the risk
ladder, temporal correlation, the kill switch and the evidence kept per session.
The caller supplies the ID, in this precedence (`internal/proxy/proxy.go`,
`resolveSessionID`): the session header (`X-Session-ID` or `session.header`),
then `X-Claude-Code-Session-Id` when its value is safe, then a value derived from
the request body, then a hash of the client address and backend.

Two attacks follow, and the second is the more serious.

- **Collision.** Session IDs are not namespaced per caller. Two callers that send
  the same ID share one session, its score and its evidence. In a shared
  deployment one caller can push another's session toward throttling or a kill.
- **Risk reset.** A caller that sends a new ID with every request accumulates
  nothing. Slow-burn and multi-message correlation cannot fire at all, with no
  clever payload needed.

Proxy authentication does not help, because it is one shared key
(`proxy.auth.api_key`, checked by `validateProxyAuth`, which returns a bool and
records no caller identity). Everyone presents the same string.

## What a credential can and cannot fix

A per-client credential fixes **collision** completely: the ID becomes
`<client>:<id>`, so two callers can never share a session.

It does **not** fix **risk reset** on its own. A keyed caller can still rotate
its own IDs and reset its own history. Fixing that needs risk to accumulate on
the credential as well as on the session ID. That is a second, larger change and
is split out below so it can be accepted or declined separately.

## Part A: keyed identity (fixes collision)

**Config.** An opt-in list beside the existing key, which keeps working:

```yaml
proxy:
  auth:
    enabled: true
    api_key: ""            # existing single shared key; unchanged
    keys:                  # new; optional
      - name: team-a
        key: ${TEAM_A_KEY}
      - name: team-b
        key: ${TEAM_B_KEY}
```

**Behavior.**
- A request authenticates with the shared key or any listed key, compared in
  constant time as today.
- `validateProxyAuth` gains a sibling that returns the matched client name. A
  request that matched a listed key carries that name through the request.
- The session ID for a keyed request is `<name>:<id>`, where `<id>` is whatever
  `resolveSessionID` produced today. An unkeyed request (shared key, trusted
  network) keeps today's ID, so nothing changes for existing deployments.
- The client name is recorded on the session and shown in the control API and
  the dashboard, so a session is attributable to a caller.
- Config validation rejects duplicate names, empty keys, a name containing `:`,
  and a key equal to the shared key.

**Not in Part A.** No change to how risk is scored. No new default: with no
`keys` configured the code path is not taken.

## Part B: per-credential risk (fixes reset)

Keep a second accumulator keyed on the client name, fed by the same events that
feed the session's. The risk ladder reads the larger of the two. Rotating
session IDs then no longer resets the credential's score, so slow-burn
detection fires for a keyed caller.

This needs decisions Part A does not:
- how the credential score decays (the session score has a half-life),
- whether a credential-level block or kill affects all of that client's
  sessions, which is a much bigger lever than a session kill,
- memory bounds, since client names are operator-defined and few, so this is
  cheap, but the accumulator needs the same eviction discipline as sessions.

Part B is where S6 (correlation along the caller axis) lives. I would not build
it without Part A shipped and used first.

## Migration

None required. Existing configs, with the single shared key or none, behave
exactly as today. A deployment adopts keys by adding the list and giving each
client its key. Sessions started before adoption keep their old IDs until they
end.

## Tests I would write first

- Two clients sending the same `X-Session-ID` get two sessions, and a kill on
  one leaves the other running.
- A shared-key or trusted-network request keeps today's ID.
- An unknown key is rejected exactly as a wrong shared key is.
- Constant-time comparison is used for every configured key.
- Validation rejects the four bad configurations above.
- The client name survives the Redis wire format and the history record.

## Risks

- **Keyless clients stay exposed.** Anything that authenticates with the shared
  key or sits on a trusted network keeps the old weakness. Part A narrows the
  problem; it does not remove it unless every caller is keyed.
- **A second place identity is decided.** `resolveSessionID` and the auth check
  must agree on the client; a bug there lets one client's request land in
  another's namespace. The collision test above guards it.
- **Key management is now the operator's job:** rotation, revocation, storage.
  This proposal puts keys in config and environment variables as the shared key
  is today. It does not add a key store.

## Decisions needed

1. Build Part A only, or A then B?
2. Should a credential-level kill (Part B) be allowed to end every session of a
   client, or only raise that client's score?
3. Is `<name>:<id>` the right ID shape, given IDs appear in logs and the
   dashboard, or should the client be a separate field with the ID unchanged?
