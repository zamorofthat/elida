export function StateBadge({ state }) {
  return <span class={'state-badge state-' + state}>{state}</span>
}

export function SeverityBadge({ severity }) {
  return <span class={'severity-badge severity-' + severity}>{severity}</span>
}

export function ProtocolBadge({ protocol }) {
  return <span class="protocol-badge">{protocol}</span>
}

const FINGERPRINT_BUCKET_COLORS = {
  normal: 'var(--text-muted, #8b8fa3)',
  minor: '#4f8ef7',
  notable: '#e0b341',
  anomalous: '#e07b41',
  severe: '#e04141',
}

export function FingerprintBadge({ bucket, distance }) {
  if (!bucket || bucket === 'warm_up') return null
  return (
    <span
      class="fingerprint-badge"
      style={{ color: FINGERPRINT_BUCKET_COLORS[bucket] || FINGERPRINT_BUCKET_COLORS.normal }}
      title={distance != null ? `Mahalanobis distance ${distance.toFixed(2)}` : bucket}
    >
      {bucket}{distance != null ? ` ${distance.toFixed(1)}` : ''}
    </span>
  )
}
