import type { ReactNode } from 'react'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

/**
 * The one status vocabulary for the whole app. Every state pill a page shows —
 * pool state, event handling state, certificate state, channel health — picks
 * one of these semantics instead of hand-rolling its own colors, so "amber
 * means attention" reads the same everywhere.
 */
export type StatusTone = 'success' | 'warning' | 'destructive' | 'info' | 'neutral'

const TONES: Record<StatusTone, string> = {
  success: 'border-emerald-600/30 bg-emerald-500/10 text-emerald-700 dark:border-emerald-500/40 dark:bg-emerald-500/15 dark:text-emerald-400',
  warning: 'border-amber-600/40 bg-amber-500/10 text-amber-700 dark:border-amber-500/40 dark:bg-amber-500/15 dark:text-amber-400',
  destructive: 'border-destructive/30 bg-destructive/10 text-destructive dark:bg-destructive/15',
  info: 'border-sky-600/30 bg-sky-500/10 text-sky-700 dark:border-sky-500/40 dark:bg-sky-500/15 dark:text-sky-400',
  neutral: 'border-border bg-muted text-muted-foreground',
}

export function StatusBadge({
  tone = 'neutral',
  className,
  children,
  ...props
}: { tone?: StatusTone; children: ReactNode } & React.ComponentProps<'span'>) {
  return (
    <Badge
      variant='outline'
      className={cn('gap-1.5 border-solid font-normal', TONES[tone], className)}
      {...props}
    >
      {children}
    </Badge>
  )
}

/** The dot-and-label form for dense tables; the label stays accessible. */
export function StatusDot({
  tone = 'neutral',
  label,
  className,
}: {
  tone?: StatusTone
  label: string
  className?: string
}) {
  const colors: Record<StatusTone, string> = {
    success: 'bg-emerald-500',
    warning: 'bg-amber-500',
    destructive: 'bg-destructive',
    info: 'bg-sky-500',
    neutral: 'bg-muted-foreground/50',
  }
  return (
    <span className={cn('inline-flex items-center gap-1.5 text-xs', className)}>
      <span aria-hidden className={cn('size-1.5 rounded-full', colors[tone])} />
      {label}
    </span>
  )
}
