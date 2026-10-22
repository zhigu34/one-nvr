import { useEffect, useMemo, useRef, useState } from 'react'
import { configuredChannels } from '@/features/foundation/channels'
import { Link } from '@tanstack/react-router'
import {
  History,
  Loader2,
  Maximize,
  Minimize,
  MonitorPlay,
  RefreshCw,
  Settings,
  Square,
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

/** Grid-shaped glyph for the layout switcher, filled up to the cell count. */
function LayoutGlyph({ layout }: { layout: Layout }) {
  const cells = { 1: 1, 4: 4, 9: 9, 16: 16 }[layout]
  const side = Math.sqrt(cells)
  return (
    <span
      aria-hidden
      className='grid gap-px'
      style={{ gridTemplateColumns: `repeat(${side}, 1fr)` }}
    >
      {Array.from({ length: cells }, (_, index) => (
        <span key={index} className='size-1.5 rounded-[1px] bg-current' />
      ))}
    </span>
  )
}

/** The one status dot per tile: green playing, amber reconnecting, red error. */
function TileDot({ state, paused }: { state: PlayerState; paused: boolean }) {
  const tone = paused
    ? 'bg-zinc-400'
    : state.phase === 'playing'
      ? 'bg-emerald-400'
      : state.phase === 'error'
        ? 'bg-red-500'
        : 'bg-amber-400'
  const text = paused
    ? '已暂停'
    : state.phase === 'playing'
      ? '播放中'
      : state.phase === 'error'
        ? '连接失败'
        : '连接中'
  return (
    <span
      aria-label={text}
      title={text}
      className='inline-flex size-2 shrink-0 rounded-full'
    >
      <span aria-hidden className={cn('size-2 animate-none rounded-full', tone)} />
    </span>
  )
}

function Tile({
  channel,
  selected,
  paused,
  dropActive,
  zoomed,
  initialStream,
  slotIndex,
  onSelect,
  onClose,
  onZoom,
  onStreamChange,
}: {
  channel?: Schema<'Channel'>
  selected: boolean
  paused: boolean
  dropActive: boolean
  /** The workspace's single-tile zoom mode owns the stream choice. */
  zoomed: boolean
  /** The slot's remembered stream choice; zoom forces main regardless. */
  initialStream: 'main' | 'sub'
  slotIndex: number
  onSelect: () => void
  onClose: () => void
  onZoom: () => void
  onStreamChange: (stream: 'main' | 'sub') => void
}) {
  const video = useRef<HTMLVideoElement>(null)
  const container = useRef<HTMLElement>(null)
  const channelID = channel?.id
  const [localStream, setLocalStream] = useState<'main' | 'sub'>(initialStream)
  // Double-click zoom forces the main stream; the workspace hands the choice
  // back on restore via onStreamChange.
  const stream = zoomed ? 'main' : localStream
  const [audio, setAudio] = useState(false)
  const [retry, setRetry] = useState(0)
  const [state, setState] = useState<PlayerState>({
    phase: 'connecting',
    message: '正在连接…',
  })
  const [media, setMedia] = useState<LiveMediaInfo | null>(null)
  const [fullscreen, setFullscreen] = useState(false)
  useEffect(() => {
    const changed = () =>
      setFullscreen(document.fullscreenElement === container.current)
    document.addEventListener('fullscreenchange', changed)
    return () => document.removeEventListener('fullscreenchange', changed)
  }, [])
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
  const live = !paused && state.phase === 'playing'
  return (
    <section
      ref={container}
      aria-label={channel ? `预览 ${label(channel)}` : '空闲画面'}
      onClick={onSelect}
      onDoubleClick={() => channel && onZoom()}
      className={cn(
        'group relative h-full min-w-0 overflow-hidden rounded-lg border bg-black text-zinc-100 transition-shadow duration-150',
        selected ? 'ring-2 ring-primary' : 'border-video-border',
        dropActive && 'ring-2 ring-sky-400',
        !channel && 'border-dashed border-border bg-muted/20'
      )}
    >
      {/* The picture fills the cell. The controls float over it, because a
          permanent bar at the bottom took height away from every stream — on a
          16-cell grid that was a measurable loss of pixels on all sixteen. */}
      <div data-testid='live-video-area' className='absolute inset-0'>
        <video
          ref={video}
          data-testid='live-video'
          autoPlay
          playsInline
          muted={!selected || !audio}
          className='h-full w-full object-contain'
        />
      </div>

      {/* Top bar: always visible, a gradient rather than a panel, so a wall of
          tiles still reads at a glance without a black strip on each one. */}
      <div className='pointer-events-none absolute inset-x-0 top-0 z-10 flex items-start justify-between gap-2 bg-gradient-to-b from-black/75 to-transparent p-2 text-xs'>
        <span className='flex min-w-0 items-center gap-1.5'>
          {channel && <TileDot state={state} paused={paused} />}
          <span className='truncate'>{channel ? label(channel) : '空闲画面'}</span>
        </span>
        {channel && (
          <span className='flex shrink-0 items-center gap-1.5'>
            {live && media && (
              <span
                data-testid='live-media-info'
                className='truncate font-mono text-[11px] text-white/70'
                title={liveMediaTitle(media)}
              >
                {liveMediaLabel(media)}
              </span>
            )}
            <Button
              variant='ghost'
              size='icon'
              aria-label={`关闭 ${label(channel)}`}
              className='pointer-events-auto size-6 rounded p-1 text-white/80 hover:bg-white/15 hover:text-white'
              onClick={(e) => {
                e.stopPropagation()
                onClose()
              }}
            >
              <X size={14} />
            </Button>
          </span>
        )}
      </div>

      {(!channel || paused || state.phase !== 'playing') && (
        <div className='absolute inset-0 flex flex-col items-center justify-center gap-3 px-4 text-center text-xs text-zinc-400'>
          {!channel ? (
            <>
              {/* An empty slot is a target, not a placeholder: the dashed border
                  and the matte icon say "drop a camera here", and the hover
                  brightens both so the slot responds before the drag lands. */}
              <MonitorPlay
                size={26}
                className='text-zinc-600 transition-colors group-hover:text-zinc-400'
              />
              <p role='status' className='text-zinc-500'>
                空画面 #{slotIndex + 1}
              </p>
              <p className='text-[11px] text-zinc-600'>
                点击或拖拽左侧通道至此
              </p>
            </>
          ) : (
            <>
              {state.phase === 'connecting' || state.phase === 'retrying' ? (
                <Loader2 size={26} className='animate-spin' />
              ) : (
                <MonitorPlay size={26} />
              )}
              <p role='status'>{message}</p>
              {!paused && state.phase === 'error' && (
                <Button
                  size='sm'
                  variant='secondary'
                  onClick={() => setRetry((v) => v + 1)}
                >
                  <RefreshCw size={14} />
                  重新连接
                </Button>
              )}
            </>
          )}
        </div>
      )}

      {/* Bottom controls: hover-only, so the stream is unobstructed until the
          operator asks for them. Focus-within keeps them reachable by keyboard. */}
      {channel && (
        <div
          className={cn(
            'absolute inset-x-2 bottom-2 z-10 flex items-center justify-between gap-2 rounded-lg bg-black/60 p-1.5 text-[11px] backdrop-blur-sm transition-opacity duration-150',
            fullscreen ? 'opacity-100' : 'opacity-0 focus-within:opacity-100 group-hover:opacity-100'
          )}
        >
          <span className='flex min-w-0 items-center gap-1.5 pl-1'>
            <span className={cn('shrink-0', live ? 'text-emerald-400' : 'text-zinc-400')}>
              {live
                ? state.fallback
                  ? '主流 · 未配置子流'
                  : state.stream === 'main'
                    ? '主流'
                    : '子流'
                : '未播放'}
            </span>
            {/* No bit rate here: the browser's own stats are the only honest
                source for one, and this tile does not read them. What it does
                know is that the stream is passed through untranscoded. */}
            {live && (
              <span className='shrink-0 text-zinc-500'>直通</span>
            )}
          </span>
          <span className='flex shrink-0 items-center gap-1'>
            <SelectField
              ariaLabel={`${label(channel)} 码流`}
              size='sm'
              triggerClassName='h-7 w-fit border-white/20 bg-white/10 px-2 text-xs text-white hover:bg-white/15'
              value={stream}
              disabled={zoomed}
              onValueChange={(next) => {
                setLocalStream(next as 'main' | 'sub')
                onStreamChange(next as 'main' | 'sub')
              }}
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
              className='size-7 p-1 text-white hover:bg-white/15 disabled:opacity-30'
            >
              {audio && selected ? <Volume2 size={14} /> : <VolumeX size={14} />}
            </Button>
            {/* The selected tile is the one whose playback link lives in the
                rail; surfacing it here puts it where the operator is looking. */}
            {channel.permissions.includes('playback') && (
              <Button
                asChild
                variant='ghost'
                size='icon'
                className='size-7 p-1 text-white hover:bg-white/15'
              >
                <Link
                  to='/recordings'
                  search={{
                    channel: channel.id ?? '',
                    at: new Date().toISOString(),
                    tab: 'playback',
                  }}
                  aria-label={`${label(channel)} 即时回放`}
                  title='即时回放'
                >
                  <History size={14} />
                </Link>
              </Button>
            )}
            <Button
              variant='ghost'
              size='icon'
              aria-label={fullscreen ? '退出全屏' : '全屏画面'}
              className='size-7 p-1 text-white hover:bg-white/15'
              onClick={() => {
                if (fullscreen) void document.exitFullscreen().catch(() => {})
                else void container.current?.requestFullscreen().catch(() => {})
              }}
            >
              {fullscreen ? <Minimize size={14} /> : <Maximize size={14} />}
            </Button>
          </span>
        </div>
      )}
    </section>
  )
}

export function LivePreview({
  initialChannel = '',
}: {
  initialChannel?: string
}) {
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
          initialChannel={initialChannel}
        />
      )}
    </>
  )
}
function LiveWorkspace({
  items,
  userID,
  initialChannel = '',
}: {
  items: Schema<'Channel'>[]
  userID: string
  initialChannel?: string
}) {
  const channels = configuredChannels(items.filter((c) => c.permissions.includes('live')))
  const storageKey = `one-nvr:live-view:${userID}`
  const [view, setView] = useState<View>(() => {
    let raw: string | null = null
    try {
      raw = localStorage.getItem(storageKey)
    } catch {
      /* private mode */
    }
    const restored = restoreView(
      raw,
      channels.map((c) => c.id)
    )
    // A deep link (?channel=) pre-fills the first empty slot with that
    // channel at mount, before any stream is negotiated; it never starts
    // media beyond what the normal empty-slot flow already does. Already
    // visible or unpermitted channels are ignored.
    if (
      initialChannel &&
      !restored.channels.slice(0, restored.layout).includes(initialChannel) &&
      channels.some((c) => c.id === initialChannel)
    ) {
      const next = [...restored.channels]
      const slot = next
        .slice(0, restored.layout)
        .findIndex((id) => !id)
      next[slot === -1 ? 0 : slot] = initialChannel
      return { ...restored, channels: next }
    }
    return restored
  })
  const [selected, setSelected] = useState(0)
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(0)
  const [paused, setPaused] = useState(document.hidden)
  const [drag, setDrag] = useState<number | null>(null)
  const [dropTarget, setDropTarget] = useState<number | null>(null)
  // Double-click zoom: the slot index shown maximised, or null for the
  // saved grid layout. Zoomed mode forces the main stream on that tile.
  const [zoomed, setZoomed] = useState<number | null>(null)
  // The operator's per-slot stream choice; survives zoom round-trips.
  const [slotStreams, setSlotStreams] = useState<Record<number, 'main' | 'sub'>>({})
  const grid = useRef<HTMLDivElement>(null)
  // Channel summaries power the rail's status lights (source errors /
  // unconfigured slots), the same derivation the recordings page uses.
  const summariesQuery = useAPI<{ items: Schema<'ChannelSummary'>[] }>(
    '/api/v1/channels/summary'
  )
  const summaries = useMemo(() => {
    const map = new Map<string, Schema<'ChannelSummary'>>()
    for (const item of summariesQuery.data?.items || []) {
      map.set(item.channel_id, item)
    }
    return map
  }, [summariesQuery.data])
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
  const anyPlaying = displayed.some((t) => !!t)
  return (
    <div className='flex h-[calc(100svh-5rem)] min-h-0 flex-col gap-3'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div className='flex items-center gap-3'>
          <h1 className='text-xl font-semibold tracking-tight'>实时预览</h1>
          <span
            role='note'
            aria-label='预览使用说明'
            title='选择画面后点击通道，默认播放子码流；双击画面放大并临时切主码流，再次双击恢复。关闭录像仍可预览。'
            className='grid size-5 cursor-help place-items-center rounded-full border border-border text-[10px] text-muted-foreground'
          >
            ?
          </span>
        </div>
        <div className='flex flex-wrap gap-2'>
          <div
            role='toolbar'
            aria-label='分屏布局'
            className='inline-flex items-center gap-0.5 rounded-lg border border-border bg-muted/30 p-0.5'
          >
            {([1, 4, 9, 16] as Layout[]).map((layout) => {
              const active = view.layout === layout
              return (
                <button
                  key={layout}
                  type='button'
                  aria-pressed={active}
                  aria-label={`${layout} 画面`}
                  title={`${layout} 画面`}
                  onClick={() => {
                    change({ ...view, layout })
                    setSelected(0)
                  }}
                  className={cn(
                    'inline-flex h-8 items-center gap-1.5 rounded-md px-2.5 text-xs transition-colors',
                    active
                      ? 'bg-background font-medium text-foreground shadow-sm ring-1 ring-border'
                      : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground'
                  )}
                >
                  <LayoutGlyph layout={layout} />
                  {layout}
                </button>
              )
            })}
          </div>
          <Button
            variant='ghost'
            size='sm'
            className='h-9'
            disabled={!anyPlaying}
            onClick={() => change({ ...view, channels: [] })}
          >
            <Square size={14} />
            停止全部
          </Button>
          <Button
            variant='ghost'
            size='sm'
            className='h-9'
            disabled={
              !channels.some(
                (c) =>
                  c.enabled !== false &&
                  summaries.get(c.id)?.current_revision_id != null
              )
            }
            onClick={() => {
              // Auto-fill: walk the usable channels in order into every empty
              // slot. "Usable" means enabled *and* configured: an empty slot
              // with no camera behind it produces a black tile and a failure
              // message, which is worse than leaving the slot empty.
              const next = [...view.channels]
              const used = new Set(next.slice(0, view.layout))
              const queue = channels.filter(
                (c) =>
                  c.enabled !== false &&
                  !used.has(c.id) &&
                  summaries.get(c.id)?.current_revision_id != null
              )
              for (let slot = 0; slot < view.layout && queue.length; slot++) {
                if (next[slot]) continue
                next[slot] = queue.shift()!.id
              }
              change({ ...view, channels: next })
            }}
          >
            <LayoutGlyph layout={4} />
            自动填充
          </Button>
          <Button
            variant='ghost'
            size='sm'
            className='h-9'
            onClick={() =>
              void grid.current?.requestFullscreen().catch(() => {})
            }
          >
            <Maximize size={14} />
            全屏
          </Button>
        </div>
      </div>
      <div className='flex min-h-0 flex-1 gap-3'>
        <aside className='flex w-60 shrink-0 flex-col gap-3 rounded-xl border bg-card p-3'>
          <div className='flex items-center justify-between text-sm font-medium'>
            <span>通道列表</span>
            <div className='flex items-center gap-1.5'>
              <span className='text-muted-foreground'>{channels.length} 路</span>
              <Link
                to='/channels'
                search={{ batch: '' }}
                aria-label='通道管理'
                title='配置通道与录像'
                className='grid size-6 place-items-center rounded-md border border-border text-muted-foreground hover:bg-accent hover:text-foreground'
              >
                <Settings size={14} />
              </Link>
            </div>
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
          <div className='grid min-h-0 flex-1 grid-cols-2 content-start gap-1 overflow-y-auto xl:grid-cols-1'>
            {filtered.slice(page * 16, (page + 1) * 16).map((channel) => {
              const summary = summaries.get(channel.id)
              const disabled = channel.enabled === false
              const hasError = !!summary?.last_error
              const unconfigured = !summary?.current_revision_id
              const dot = disabled
                ? { cls: 'bg-zinc-600', title: '已停用' }
                : hasError
                  ? { cls: 'bg-red-500', title: `源异常：${summary?.last_error}` }
                  : unconfigured
                    ? { cls: 'bg-zinc-600', title: '未配置源' }
                    : {
                        cls: 'bg-emerald-500 shadow-[0_0_5px_rgba(16,185,129,0.7)]',
                        title: '源在线',
                      }
              const slot = view.channels.indexOf(channel.id)
              const onGrid = slot >= 0 && slot < view.layout
              return (
                <Button
                  key={channel.id}
                  variant='ghost'
                  draggable
                  aria-label={label(channel)}
                  onDragStart={(e) =>
                    e.dataTransfer.setData('text/plain', channel.id)
                  }
                  onClick={() => {
                    choose(channel.id)
                    // Clicking an on-grid channel jumps the focus to its slot.
                    if (onGrid) setSelected(slot)
                  }}
                  className={cn(
                    'h-auto w-full justify-start gap-2 truncate px-2 py-2 text-left text-sm font-normal transition-colors',
                    displayed[selected]?.id === channel.id &&
                      'bg-accent font-medium ring-1 ring-border',
                    unconfigured && 'text-muted-foreground/60'
                  )}
                >
                  <span
                    aria-hidden
                    title={dot.title}
                    className={cn(
                      'relative inline-block size-1.5 shrink-0 rounded-full',
                      dot.cls
                    )}
                  >
                    {/* Only the channel on screen pulses: a still dot says "the
                        source is up", a pulsing one says "you are watching it". */}
                    {onGrid && (
                      <span className='absolute inset-0 animate-ping rounded-full bg-emerald-500 opacity-60 motion-reduce:hidden' />
                    )}
                  </span>
                  <span className='inline-grid min-w-5 shrink-0 place-items-center rounded bg-black/10 px-1 text-[10px] font-semibold tabular-nums dark:bg-white/10'>
                    {String(channel.channel_no).padStart(2, '0')}
                  </span>
                  <span className='flex-1 truncate'>{channel.channel_name}</span>
                  {/* Position and group are context, not status: green on every
                      row spent the operator's attention on twelve identical
                      chips, so a row that is actually playing had nothing left
                      to stand out with. */}
                  {onGrid && (
                    <span className='shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] tabular-nums text-muted-foreground'>
                      画面 {slot + 1}
                    </span>
                  )}
                  {channel.channel_group && (
                    <span
                      title={`分组 · ${channel.channel_group}`}
                      className='shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground'
                    >
                      {channel.channel_group}
                    </span>
                  )}
                </Button>
              )
            })}
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
          {displayed[selected]?.permissions.includes('playback') && (
            <Button asChild variant='link' className='h-auto p-0'>
              <Link
                to='/recordings'
                search={{
                  channel: displayed[selected]!.id,
                  at: new Date().toISOString(),
                  // The instant-playback entry belongs to the playback tab.
                  tab: 'playback',
                }}
              >
                查看 {label(displayed[selected]!)} 即时回放
              </Link>
            </Button>
          )}
        </aside>
        <div
          ref={grid}
          aria-label='预览画面网格'
          style={
            zoomed !== null
              ? {
                  gridTemplateColumns: 'minmax(0, 1fr)',
                  gridTemplateRows: 'minmax(0, 1fr)',
                }
              : {
                  gridTemplateColumns: `repeat(${view.layout === 1 ? 1 : view.layout === 4 ? 2 : view.layout === 9 ? 3 : 4}, minmax(0, 1fr))`,
                  gridTemplateRows: `repeat(${view.layout === 1 ? 1 : view.layout === 4 ? 2 : view.layout === 9 ? 3 : 4}, minmax(0, 1fr))`,
                }
          }
          className='grid h-full min-h-0 flex-1 gap-2 overflow-hidden rounded-xl bg-video-surface p-2'
        >
          {(zoomed !== null ? [zoomed] : Array.from({ length: view.layout }, (_, i) => i)).map((index) => (
            <div
              key={index}
              className='flex min-h-0 min-w-0 flex-col overflow-hidden'
              draggable={!!displayed[index]}
              onDragStart={() => setDrag(index)}
              onDragEnd={() => {
                setDrag(null)
                setDropTarget(null)
              }}
              onDragOver={(e) => {
                e.preventDefault()
                setDropTarget(index)
              }}
              onDragLeave={() =>
                setDropTarget((current) => (current === index ? null : current))
              }
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
                setDropTarget(null)
              }}
            >
              <Tile
                key={`${index}:${displayed[index]?.id || 'empty'}:${zoomed === index ? 'main' : slotStreams[index] ?? 'sub'}`}
                channel={displayed[index]}
                selected={selected === index}
                paused={paused}
                dropActive={dropTarget === index && drag !== null}
                zoomed={zoomed === index}
                initialStream={slotStreams[index] ?? 'sub'}
                slotIndex={index}
                onSelect={() => setSelected(index)}
                onClose={() => {
                  const next = [...view.channels]
                  next[index] = ''
                  change({ ...view, channels: next })
                }}
                onZoom={() => setZoomed((current) => (current === index ? null : index))}
                onStreamChange={(stream) => {
                  // Remember the operator's stream choice per slot; restoring
                  // the grid hands it back to the tile.
                  setSlotStreams((previous) => {
                    const next = { ...previous, [index]: stream }
                    return next
                  })
                }}
              />
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
