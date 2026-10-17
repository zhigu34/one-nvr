/**
 * Sparkbar draws the three-way split of a channel's observed time.
 *
 * The point is to make an anomalous row findable without reading three numbers:
 * `CH07 天台` at 31 minutes unavailable out of a day is a thin red sliver, and
 * that is exactly what makes it visible in a column of otherwise solid green.
 *
 * The bar describes observed time, not the window — sampleNote already says how
 * much of the window that was. A row with no observation draws nothing rather
 * than an empty track, because a grey bar would read as "all unknown" instead of
 * "nothing was observed".
 *
 * Segments use the same colours as the numbers they stand for (emerald for
 * healthy, red for unavailable, amber for unknown), and each carries a title so
 * the ratio is still readable without colour.
 */
import { spanShares } from './operations-format'

export function Sparkbar({
  healthyMs,
  unavailableMs,
  unknownMs,
}: {
  healthyMs: number
  unavailableMs: number
  unknownMs: number
}) {
  const shares = spanShares({
    healthy_ms: healthyMs,
    unavailable_ms: unavailableMs,
    unknown_ms: unknownMs,
  })
  if (!shares) return null
  const segments = [
    { key: 'healthy', share: shares.healthy, className: 'bg-emerald-500/80' },
    { key: 'unavailable', share: shares.unavailable, className: 'bg-red-500/80' },
    { key: 'unknown', share: shares.unknown, className: 'bg-zinc-500/60' },
  ].filter((segment) => segment.share > 0)
  return (
    <div
      className='flex h-1.5 w-full overflow-hidden rounded-full bg-muted'
      role='img'
      aria-label={`可用 ${(shares.healthy * 100).toFixed(1)}%，不可用 ${(
        shares.unavailable * 100
      ).toFixed(1)}%，未知 ${(shares.unknown * 100).toFixed(1)}%`}
    >
      {segments.map((segment) => (
        <div
          key={segment.key}
          className={segment.className}
          style={{ width: `${segment.share * 100}%` }}
        />
      ))}
    </div>
  )
}
