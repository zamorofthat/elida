export function StateBadge({ state }) {
  return <span class={'state-badge state-' + state}>{state}</span>
}

export function SeverityBadge({ severity }) {
  return <span class={'severity-badge severity-' + severity}>{severity}</span>
}

export function ProtocolBadge({ protocol }) {
  return <span class="protocol-badge">{protocol}</span>
}
