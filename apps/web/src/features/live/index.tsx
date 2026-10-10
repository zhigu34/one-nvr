import { useEffect, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import {
  Maximize,
  MonitorPlay,
  RefreshCw,
  Volume2,
  VolumeX,
  X,
} from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useAPI } from '@/features/foundation/hooks'
import { QueryState, SelectField } from '@/features/foundation/ui'
import { cn } from '@/lib/utils'
import { LivePlayer, type PlayerState } from './player'
import {
  liveMediaLabel,
  liveMediaTitle,
  type LiveMediaInfo,
} from './media-info'
import { restoreView, type Layout, type View } from './view'

function label(c: Schema<'Channel'>) {
  return `CH${String(c.channel_no).padStart(2, '0')} ${c.channel_name}`
}
function Tile({
  channel,
  selected,
  paused,
  onSelect,
  onClose,
}: {
  channel?: Schema<'Channel'>
  selected: boolean
  paused: boolean
  onSelect: () => void
  onClose: () => void
}) {
  const video = useRef<HTMLVideoElement>(null)
  const container = useRef<HTMLElement>(null)
  const channelID = channel?.id
  const [stream, setStream] = useState<'main' | 'sub'>('sub')
  const [audio, setAudio] = useState(false)
  const [retry, setRetry] = useState(0)
  const [state, setState] = useState<PlayerState>({
    phase: 'connecting',
    message: '正在连接…',
  })
  const [media, setMedia] = useState<LiveMediaInfo | null>(null)
  useEffect(() => {
    if (!channelID || paused || !video.current) return
    const player = new LivePlayer(
      video.current,
      channelID,
      stream,
      setState,
      setMedia
    )
    void player.start()
    return () => player.stop()
  }, [channelID, paused, stream, retry])
  const message = paused ? '页面在后台，预览已暂停' : state.message
  return (
    <section
      ref={container}
      aria-label={channel ? `预览 ${label(channel)}` : '空闲画面'}
      onClick={onSelect}
      className={`relative flex min-w-0 flex-col overflow-hidden rounded-lg border bg-black text-slate-100 ${selected ? 'ring-2 ring-primary' : 'border-slate-800'}`}
    >
      <div className='flex min-h-9 items-center justify-between gap-2 bg-slate-950 px-2 text-xs'>
        <span className='truncate'>
          {channel ? label(channel) : '选择通道开始预览'}
        </span>
        {channel && (
          <Button
            variant='ghost'
            size='icon'
            aria-label={`关闭 ${label(channel)}`}
            className='size-6 rounded p-1 text-slate-100 hover:bg-slate-800 hover:text-slate-100'
            onClick={(e) => {
              e.stopPropagation()
              onClose()
            }}
          >
            <X size={14} />
          </Button>
        )}
      </div>
      <div className='relative aspect-video min-h-0 flex-1'>
        <video
          ref={video}
          data-testid='live-video'
          autoPlay
          playsInline
          muted={!selected || !audio}
          className='h-full w-full object-contain'
        />
        {(!channel || paused || state.phase !== 'playing') && (
          <div className='absolute inset-0 flex flex-col items-center justify-center gap-3 bg-slate-950/95 px-4 text-center text-xs text-slate-300'>
            <MonitorPlay size={26} />
            <p role='status'>
              {channel ? message : '点击左侧通道，放入选中的画面'}
            </p>
            {channel && !paused && state.phase === 'error' && (
              <Button
                size='sm'
                variant='secondary'
                onClick={() => setRetry((v) => v + 1)}
              >
                <RefreshCw size={14} />
                重新连接
              </Button>
            )}
          </div>
        )}
      </div>
      {channel && (
        <div className='flex flex-wrap items-center justify-between gap-2 bg-slate-950 px-2 py-1.5 text-xs'>
          <div className='flex min-w-0 flex-1 items-center gap-2'>
            <span
              className={
                state.phase === 'playing' && !paused
                  ? 'text-emerald-400'
                  : 'text-slate-400'
              }
            >
              {!paused && state.phase === 'playing'
                ? state.fallback
                  ? '主流 · 未配置子流'
                  : state.stream === 'main'
                    ? '主流'
                    : '子流'
                : '未播放'}
            </span>
            {!paused && state.phase === 'playing' && media && (
              <span
                data-testid='live-media-info'
                className='truncate text-slate-300'
                title={liveMediaTitle(media)}
              >
                {liveMediaLabel(media)}
              </span>
            )}
          </div>
          <div className='flex items-center gap-2'>
            <SelectField
              ariaLabel={`${label(channel)} 码流`}
              size='sm'
              className='w-28'
              triggerClassName='text-xs'
              value={stream}
              onValueChange={(next) => setStream(next as 'main' | 'sub')}
              options={[
                { value: 'sub', label: '子码流' },
                { value: 'main', label: '主码流' },
              ]}
            />
            <Button
              variant='ghost'
              size='icon'
              aria-label='切换声音'
              disabled={!selected}
              onClick={() => setAudio((v) => !v)}
              className='size-6 p-1 text-slate-100 hover:bg-slate-800 hover:text-slate-100 disabled:opacity-30'
            >
              {audio && selected ? (
                <Volume2 size={14} />
              ) : (
                <VolumeX size={14} />
              )}
            </Button>
            <Button
              variant='ghost'
              size='icon'
              aria-label='全屏画面'
              className='size-6 p-1 text-slate-100 hover:bg-slate-800 hover:text-slate-100'
              onClick={() =>
                void container.current?.requestFullscreen().catch(() => {})
              }
            >
              <Maximize size={14} />
            </Button>
          </div>
        </div>
      )}
    </section>
  )
}

export function LivePreview() {
  const query = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100',
    true,
    5000
  )
  const user = useAuthStore((s) => s.user)
  return (
    <>
      <QueryState
        pending={query.isPending}
        error={query.error}
        retry={query.refetch}
      />
      {query.data && (
        <LiveWorkspace
          key={user?.id}
          items={query.data.items}
          userID={user?.id || ''}
        />
      )}
    </>
  )
}
function LiveWorkspace({
  items,
  userID,
}: {
  items: Schema<'Channel'>[]
  userID: string
}) {
  const channels = items.filter((c) => c.permissions.includes('live'))
  const storageKey = `one-nvr:live-view:${userID}`
  const [view, setView] = useState<View>(() => {
    let raw: string | null = null
    try {
      raw = localStorage.getItem(storageKey)
    } catch {
      /* private mode */
    }
    return restoreView(
      raw,
      channels.map((c) => c.id)
    )
  })
  const [selected, setSelected] = useState(0)
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(0)
  const [paused, setPaused] = useState(document.hidden)
  const [drag, setDrag] = useState<number | null>(null)
  const grid = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const changed = () => setPaused(document.hidden)
    document.addEventListener('visibilitychange', changed)
    return () => document.removeEventListener('visibilitychange', changed)
  }, [])
  const displayed = view.channels
    .slice(0, view.layout)
    .map((id) => channels.find((c) => c.id === id))
  function change(next: View) {
    setView(next)
    try {
      localStorage.setItem(storageKey, JSON.stringify(next))
    } catch {
      /* private mode */
    }
  }
  function choose(id: string) {
    const next = [...view.channels]
    const previous = next.indexOf(id)
    if (previous >= 0 && previous < view.layout && previous !== selected) {
      setSelected(previous)
      return
    }
    if (previous >= view.layout) next[previous] = ''
    next[selected] = id
    change({ ...view, channels: next })
  }
  const filtered = channels.filter((c) =>
    label(c).toLowerCase().includes(search.toLowerCase())
  )
  const pageCount = Math.max(1, Math.ceil(filtered.length / 16))
  return (
    <div className='space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div>
          <h1 className='text-2xl font-semibold tracking-tight'>实时预览</h1>
          <p className='mt-1 text-sm text-muted-foreground'>
            选择画面后点击通道，默认播放子码流。关闭录像仍可预览。
          </p>
        </div>
        <div className='flex flex-wrap gap-2'>
          <div
            aria-label='分屏布局'
            className='flex gap-1 rounded-lg border p-1'
          >
            {([1, 4, 9, 16] as Layout[]).map((layout) => (
              <Button
                key={layout}
                size='sm'
                variant={view.layout === layout ? 'secondary' : 'ghost'}
                onClick={() => {
                  change({ ...view, layout })
                  setSelected(0)
                }}
              >
                {layout} 画面
              </Button>
            ))}
          </div>
          <Button
            variant='outline'
            onClick={() => change({ ...view, channels: [] })}
          >
            停止全部
          </Button>
          <Button
            variant='outline'
            onClick={() =>
              void grid.current?.requestFullscreen().catch(() => {})
            }
          >
            <Maximize size={16} />
            全屏
          </Button>
        </div>
      </div>
      <div className='grid items-start gap-4 xl:grid-cols-[220px_minmax(0,1fr)]'>
        <aside className='space-y-3 rounded-xl border bg-card p-3'>
          <div className='flex items-center justify-between text-sm font-medium'>
            <span>通道列表</span>
            <span className='text-muted-foreground'>{channels.length} 路</span>
          </div>
          <Input
            aria-label='搜索预览通道'
            placeholder='搜索编号或名称'
            value={search}
            onChange={(e) => {
              setSearch(e.target.value)
              setPage(0)
            }}
          />
          <div className='grid grid-cols-2 gap-1 xl:grid-cols-1'>
            {filtered.slice(page * 16, (page + 1) * 16).map((channel) => (
              <Button
                key={channel.id}
                variant='ghost'
                draggable
                onDragStart={(e) =>
                  e.dataTransfer.setData('text/plain', channel.id)
                }
                onClick={() => choose(channel.id)}
                className={cn(
                  'h-auto w-full justify-start truncate px-2 py-2 text-left text-sm font-normal',
                  displayed[selected]?.id === channel.id &&
                    'bg-accent font-medium'
                )}
              >
                {label(channel)}
              </Button>
            ))}
          </div>
          {!channels.length && (
            <p className='text-sm text-muted-foreground'>
              没有可预览的通道，请联系管理员分配实时预览权限。
            </p>
          )}
          {pageCount > 1 && (
            <div className='flex items-center justify-between'>
              <Button
                variant='ghost'
                size='sm'
                disabled={page === 0}
                onClick={() => setPage((p) => p - 1)}
              >
                上一组
              </Button>
              <span className='text-xs'>
                {page + 1}/{pageCount}
              </span>
              <Button
                variant='ghost'
                size='sm'
                disabled={page >= pageCount - 1}
                onClick={() => setPage((p) => p + 1)}
              >
                下一组
              </Button>
            </div>
          )}
          <p className='text-xs text-muted-foreground'>
            拖动通道到画面，或拖动画面调整顺序。仅选中画面可开启声音。
          </p>
          <Link
            to='/channels'
            search={{ batch: '' }}
            className='block text-sm text-primary underline underline-offset-4'
          >
            配置通道与录像
          </Link>
          {displayed[selected]?.permissions.includes('playback') && (
            <Link
              to='/recordings'
              search={{
                channel: displayed[selected]!.id,
                at: new Date().toISOString(),
              }}
              className='block text-sm text-primary underline underline-offset-4'
            >
              查看 {label(displayed[selected]!)} 即时回放
            </Link>
          )}
        </aside>
        <div
          ref={grid}
          aria-label='预览画面网格'
          className={`grid gap-2 rounded-xl bg-slate-950 p-2 ${view.layout === 1 ? 'grid-cols-1' : view.layout === 4 ? 'grid-cols-1 sm:grid-cols-2' : view.layout === 9 ? 'grid-cols-2 lg:grid-cols-3' : 'grid-cols-2 lg:grid-cols-4'}`}
        >
          {Array.from({ length: view.layout }, (_, index) => (
            <div
              key={index}
              draggable={!!displayed[index]}
              onDragStart={() => setDrag(index)}
              onDragEnd={() => setDrag(null)}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                e.preventDefault()
                const next = [...view.channels]
                const channel = e.dataTransfer.getData('text/plain')
                if (channel && channels.some((c) => c.id === channel)) {
                  const other = next.indexOf(channel)
                  if (other >= 0)
                    [next[index], next[other]] = [next[other], next[index]]
                  else next[index] = channel
                } else if (drag !== null)
                  [next[index], next[drag]] = [next[drag], next[index]]
                change({ ...view, channels: next })
                setSelected(index)
                setDrag(null)
              }}
            >
              <Tile
                key={displayed[index]?.id || 'empty'}
                channel={displayed[index]}
                selected={selected === index}
                paused={paused}
                onSelect={() => setSelected(index)}
                onClose={() => {
                  const next = [...view.channels]
                  next[index] = ''
                  change({ ...view, channels: next })
                }}
              />
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
