import type { Schema } from '@/lib/types'

export function SourceHistory({
  items,
  selectedId,
  currentId,
  onSelect,
}: {
  items: Schema<'SourceRevision'>[]
  selectedId: string
  currentId: string | null
  onSelect: (value: Schema<'SourceRevision'>) => void
}) {
  return (
    <div className='grid gap-3'>
      <label className='grid gap-2 text-sm'>
        配置修订
        <select
          value={selectedId}
          onChange={(e) => {
            const item = items.find((item) => item.id === e.target.value)
            if (item) onSelect(item)
          }}
          className='rounded-md border bg-background p-2'
        >
          <option value=''>请选择配置修订</option>
          {items.map((item) => (
            <option key={item.id} value={item.id}>
              修订 {item.number} · {item.config.ip}{' '}
              {item.id === currentId ? '· 当前生效' : ''}
            </option>
          ))}
        </select>
      </label>
      <p className='text-xs text-muted-foreground'>
        选择历史修订仅供查看和测试；应用须显式确认。恢复旧摄像头身份时，可在草稿中选择对应历史身份。
      </p>
    </div>
  )
}
