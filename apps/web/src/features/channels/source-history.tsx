import type { Schema } from '@/lib/types'
import { SelectField } from '@/features/foundation/ui'

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
      <SelectField
        label='配置修订'
        className='text-sm'
        value={selectedId}
        onValueChange={(next) => {
          const item = items.find((entry) => entry.id === next)
          if (item) onSelect(item)
        }}
        options={[
          { value: '', label: '请选择配置修订' },
          ...items.map((item) => ({
            value: item.id,
            label: `修订 ${item.number} · ${item.config.ip}${item.id === currentId ? ' · 当前生效' : ''}`,
          })),
        ]}
      />
      <p className='text-xs text-muted-foreground'>
        选择历史修订仅供查看和测试；应用须显式确认。恢复旧摄像头身份时，可在草稿中选择对应历史身份。
      </p>
    </div>
  )
}
