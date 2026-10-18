import { WEEKDAYS } from './schedule-format'
import { dayWindowsLabel } from './mode-format'
import { cn } from '@/lib/utils'

/**
 * The seven-day mini summary for a planned row, readable without expanding
 * anything: one small square per weekday, lit when that day records, dimmed
 * when it does not. Hover names the day's windows ("周日 08:00 – 09:00"), so the
 * compact form never costs the actual times.
 *
 * `text` is the caller's pre-computed uniform time range ("08:00 – 19:00") or
 * "多时段" when the days disagree — the weekday names live in the squares
 * themselves, never repeated as prose beside them.
 */
export function WeekPills({
  windows,
  text,
  label,
}: {
  windows: { weekday: number; start_minute: number; end_minute: number }[]
  text?: string
  label: string
}) {
  const planned = new Set(windows.map((window) => window.weekday))
  return (
    <span className='inline-flex items-center gap-1.5 whitespace-nowrap'>
      <span
        className='inline-flex items-center gap-0.5'
        role='img'
        aria-label={`${label} 周计划：${
          planned.size === 0
            ? '未设置时段'
            : [...planned]
                .sort((a, b) => a - b)
                .map((day) => dayWindowsLabel(windows, day))
                .join('；')
        }`}
      >
        {WEEKDAYS.map((day) => {
          const active = planned.has(day.value)
          return (
            <span
              key={day.value}
              title={dayWindowsLabel(windows, day.value)}
              className={cn(
                'grid size-4 place-items-center rounded-[3px] text-[9px] leading-none',
                active
                  ? 'bg-emerald-500/90 font-medium text-zinc-950'
                  : 'bg-zinc-800 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-600'
              )}
            >
              {day.short}
            </span>
          )
        })}
      </span>
      {text && (
        <span className='text-xs tabular-nums text-muted-foreground'>{text}</span>
      )}
    </span>
  )
}
