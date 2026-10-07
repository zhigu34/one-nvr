import { useState } from 'react'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI } from '@/features/foundation/hooks'
import { Field, QueryState, Empty } from '@/features/foundation/ui'

const states: Record<Schema<'RecordingSegment'>['state'], string> = {
  ready: '可用',
  finalizing: '处理中',
  missing: '文件缺失',
  damaged: '文件损坏',
  conflict: '文件冲突',
  provisional: '绝对时间待确认',
}
export function RecordingsIndex({ channelId }: { channelId: string }) {
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
  const [range, setRange] = useState(() => ({
      start: new Date(Date.now() - 86400000).toISOString(),
      end: new Date().toISOString(),
    })),
    [cursor, setCursor] = useState(''),
    [error, setError] = useState('')
  const query = useAPI<Schema<'RecordingPage'>>(
    `/api/v1/recordings?${new URLSearchParams({ channel_id: channelId, start: range.start, end: range.end, limit: '100', ...(cursor ? { cursor } : {}) })}`
  )
  const zone = site.data?.timezone || 'Asia/Shanghai'
  function time(value: string) {
    return new Date(value).toLocaleString('zh-CN', {
      timeZone: zone,
      hour12: false,
    })
  }
  return (
    <section className='grid gap-4'>
      <p className='text-xs text-muted-foreground'>
        结果按站点时区 {zone} 显示。查询时间带时区偏移，例如
        2026-10-05T08:00:00+08:00。播放此通道的历史录像请到「录像回放」页面。
      </p>
      <form
        className='grid gap-3 md:grid-cols-3'
        onSubmit={(e) => {
          e.preventDefault()
          const data = new FormData(e.currentTarget),
            start = String(data.get('start')),
            end = String(data.get('end'))
          if (
            !Number.isFinite(Date.parse(start)) ||
            !Number.isFinite(Date.parse(end)) ||
            Date.parse(start) >= Date.parse(end)
          ) {
            setError('请输入有效起止时间，结束需晚于开始')
            return
          }
          setError('')
          setRange({
            start: new Date(start).toISOString(),
            end: new Date(end).toISOString(),
          })
          setCursor('')
        }}
      >
        <Field
          label='录像开始时间'
          name='start'
          defaultValue={range.start}
          required
        />
        <Field
          label='录像结束时间'
          name='end'
          defaultValue={range.end}
          required
        />
        <Button>检索录像索引</Button>
      </form>
      {error && <p role='alert'>{error}</p>}
      <QueryState
        pending={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      {query.data?.items.length === 0 && (
        <Empty>此时间范围没有已登记录像</Empty>
      )}
      {query.data && query.data.items.length > 0 && (
        <div className='overflow-x-auto'>
          <table className='w-full text-left text-sm'>
            <thead>
              <tr className='border-b'>
                <th className='p-2'>起止时间</th>
                <th className='p-2'>状态</th>
                <th className='p-2'>大小</th>
                <th className='p-2'>来源修订 / 存储池</th>
              </tr>
            </thead>
            <tbody>
              {query.data.items.map((item) => (
                <tr key={item.id} className='border-b'>
                  <td className='p-2'>
                    {time(item.start)} — {time(item.end)}
                  </td>
                  <td className='p-2'>
                    {item.check_state === 'pool_unavailable'
                      ? '存储池不可用'
                      : states[item.state]}
                  </td>
                  <td className='p-2'>
                    {(item.bytes / 1048576).toFixed(1)} MiB
                  </td>
                  <td className='p-2'>
                    {item.source_revision_id.slice(0, 8)} /{' '}
                    {item.pool_id.slice(0, 8)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {query.data?.next_cursor && (
        <Button
          variant='outline'
          onClick={() => setCursor(query.data.next_cursor!)}
        >
          下一页录像
        </Button>
      )}
      {cursor && (
        <Button variant='outline' onClick={() => setCursor('')}>
          回到首页录像
        </Button>
      )}
    </section>
  )
}
