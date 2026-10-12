import { Skeleton } from '@/components/ui/skeleton'

/**
 * Shared loading skeletons. The `QueryState` pending text stays for screen
 * readers (it is the live region announcement); these shapes are what sighted
 * users see instead of a bare "正在加载…".
 */
export function RowsSkeleton({ rows = 4 }: { rows?: number }) {
  return (
    <div aria-hidden className='space-y-2'>
      {Array.from({ length: rows }, (_, index) => (
        <Skeleton key={index} className='h-12 w-full rounded-lg' />
      ))}
    </div>
  )
}

export function CardsSkeleton({
  count = 6,
  className,
}: {
  count?: number
  className?: string
}) {
  return (
    <div
      aria-hidden
      className={`grid gap-3 sm:grid-cols-2 lg:grid-cols-3 ${className || ''}`}
    >
      {Array.from({ length: count }, (_, index) => (
        <div key={index} className='space-y-2 rounded-xl border bg-card p-3'>
          <Skeleton className='h-4 w-24' />
          <Skeleton className='aspect-video w-full rounded' />
          <Skeleton className='h-3 w-40' />
        </div>
      ))}
    </div>
  )
}

export function PanelSkeleton({ className }: { className?: string }) {
  return (
    <div
      aria-hidden
      className={`space-y-3 rounded-xl border bg-card p-4 ${className || ''}`}
    >
      <Skeleton className='h-5 w-32' />
      <Skeleton className='h-10 w-full' />
      <Skeleton className='h-10 w-full' />
    </div>
  )
}
