import { Button } from '@/components/ui/button'

/**
 * The page control shared by the fault and audit lists.
 *
 * Both lists page in ten visible rows so the two cards have the same rhythm and
 * neither grows without bound. The row count is deliberately visible: a page
 * control that only says "1 / 12" hides whether the current view is three rows or
 * a hundred, and the whole reason these lists are paged is that their unfiltered
 * size is enormous.
 */
export function Pager({
  page,
  pageCount,
  rowCount,
  onPage,
  note,
}: {
  page: number
  pageCount: number
  /** Total rows across all pages, after filtering and grouping. */
  rowCount: number
  onPage: (page: number) => void
  /** Extra context, e.g. how many records a batch row stands for. */
  note?: string
}) {
  return (
    <div className='flex flex-wrap items-center justify-between gap-3 border-t pt-3'>
      <p className='text-xs text-muted-foreground'>
        第 {page} / {Math.max(pageCount, 1)} 页（共 {rowCount} 行）
        {note ? ` · ${note}` : ''}
      </p>
      <div className='flex items-center gap-2'>
        <Button
          variant='outline'
          size='sm'
          disabled={page <= 1}
          onClick={() => onPage(page - 1)}
        >
          上一页
        </Button>
        <Button
          variant='outline'
          size='sm'
          disabled={page >= pageCount}
          onClick={() => onPage(page + 1)}
        >
          下一页
        </Button>
      </div>
    </div>
  )
}
