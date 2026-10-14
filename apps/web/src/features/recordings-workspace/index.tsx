import { useMemo, useState, useSyncExternalStore } from 'react'
import { Search } from 'lucide-react'
import { useNavigate } from '@tanstack/react-router'
import type { PageData, Schema } from '@/lib/types'
import { useAPI } from '@/features/foundation/hooks'
import { Empty, QueryState } from '@/features/foundation/ui'
import { Input } from '@/components/ui/input'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useAuthStore } from '@/stores/auth-store'
import { Playback } from '@/features/playback'
import { RecordingFiles } from '@/features/recording-files'
import { cn } from '@/lib/utils'

// The 3-column workspace needs at least 192 (rail) + ~640 (player) + 320
// (files) + a couple of gaps to be useful; below that we drop the file rail
// and surface it through a side Sheet so the playback keeps its full width.
const NARROW_BREAKPOINT = 1023
const NARROW_QUERY = `(max-width: ${NARROW_BREAKPOINT}px)`

function useNarrowViewport() {
  return useSyncExternalStore(
    (callback) => {
      const mql = window.matchMedia(NARROW_QUERY)
      mql.addEventListener('change', callback)
      return () => mql.removeEventListener('change', callback)
    },
    () => window.matchMedia(NARROW_QUERY).matches,
    () => false
  )
}

/**
 * Three-column workspace that unifies playback and file management: channels
 * on the left, playback + timeline in the center, the file list on the right.
 * A single surface replaces the old tab strip; the URL keeps `?tab=` for
 * backward compatibility with deep links but no longer drives a tab.
 */
export function RecordingsWorkspace({
  initialChannel = '',
  initialAt = '',
  initialTab = '',
}: {
  initialChannel?: string
  initialAt?: string
  initialTab?: string
}) {
  const navigate = useNavigate()
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  // `initialTab` is read so legacy links like /recordings?tab=files still load
  // without warning; the new shell has no tab to switch to.
  void initialTab
  const channelsQuery = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100'
  )
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
  const channels = (channelsQuery.data?.items || []).filter((channel) =>
    channel.permissions.includes('playback')
  )
  // The shell owns the channel choice; both panels render from this single id.
  // `resolvedID` holds the operator's pick; `fallbackID` fills in when the
  // channels query has loaded but the operator has not chosen yet. Reading a
  // derived value during render (rather than syncing state in an effect) keeps
  // the panel rendering deterministic without cascading updates.
  const [resolvedID, setResolvedID] = useState(initialChannel)
  const fallbackID =
    channels.find((channel) => channel.id === initialChannel)?.id ||
    channels[0]?.id ||
    ''
  const channelID = resolvedID || fallbackID
  // The file rail starts collapsed: the player and timeline own the screen
  // until the operator explicitly opens the list.
  const [rightOpen, setRightOpen] = useState(false)
  const [sheetOpen, setSheetOpen] = useState(false)
  const narrow = useNarrowViewport()
  // Resizing from wide → narrow leaves the rail half-collapsed; reset to
  // expanded by default on the bigger layout, leave the Sheet closed on the
  // smaller one. This avoids a flash of "panel showing partly" on resize.
  const inlineFileVisible = !narrow && rightOpen
  // The playback console reports its search range up; that one window is
  // the single time source for the file panel and the per-channel counts.
  const [activeRange, setActiveRange] = useState<{ start: number; end: number } | null>(
    null
  )
  // The segment the player currently sits on (parked or playing), reported
  // up so the file list can highlight the row and scroll it into view.
  const [activeSegmentId, setActiveSegmentId] = useState('')
  // A play request from the file list: {id, seq} — the seq makes repeated
  // plays of the same segment re-trigger the effect downstream.
  const [playSignal, setPlaySignal] = useState<{ id: string; seq: number } | null>(
    null
  )
  const onPlaySegment = (segmentId: string) =>
    setPlaySignal({ id: segmentId, seq: (playSignal?.seq ?? 0) + 1 })
  function onChannelChange(next: string) {
    if (!next || next === channelID) return
    setResolvedID(next)
    void navigate({
      to: '/recordings',
      search: {
        channel: next,
        at: initialAt,
        tab: '',
      },
    }).catch(() => {
      // No router in tests; the local state still tracks the choice.
    })
  }
  const activeChannel = useMemo(
    () => channels.find((channel) => channel.id === channelID),
    [channels, channelID]
  )
  // The header chip and the collapsed-rail badge read the active channel's
  // segment count over the playback search window — the same window the
  // file panel lists.
  const statsPath = useMemo(() => {
    if (!activeChannel || !activeRange) return ''
    return `/api/v1/recordings/stats?${new URLSearchParams({
      channel_id: activeChannel.id,
      start: new Date(activeRange.start).toISOString(),
      end: new Date(activeRange.end).toISOString(),
      limit: '100',
    })}`
  }, [activeChannel, activeRange])
  const stats = useAPI<Schema<'RecordingStats'>>(
    statsPath,
    admin && !!activeChannel && !!activeRange
  )
  // Channel summaries give the rail a real status light (source errors) and
  // a per-channel has-footage count for the active window.
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
  return (
    <div
      data-testid='recordings-workspace'
      // Exact viewport leftover: the layout chain is SidebarProvider
      // (min-h-svh — content-driven, NOT a definite height) → sticky header
      // h-16 → Main fixed py-0. Because the provider only sets a *minimum*,
      // h-full here resolves to auto and the grid's 1fr rows collapse to
      // content — the file panel then stretches row 1 beyond the viewport.
      // The only chrome above this div is the h-16 header, so an explicit
      // calc is the precise, stable value; overflow-hidden keeps the page
      // at exactly one viewport.
      className='flex h-[calc(100vh-3.5rem)] max-h-[calc(100vh-3.5rem)] min-h-0 flex-col overflow-hidden bg-video-surface text-video-chrome-foreground'
    >
      <QueryState
        pending={channelsQuery.isPending || site.isPending}
        error={channelsQuery.error || site.error}
        retry={() => {
          void channelsQuery.refetch()
          void site.refetch()
        }}
      />
      {!channels.length && channelsQuery.data && (
        <Empty>没有可回放的通道，请联系管理员分配录像回放权限。</Empty>
      )}
      {!!channels.length && (
        <Playback
          providedChannels={channels}
          controlledChannelId={channelID}
          onChannelChange={onChannelChange}
          initialChannel={channelID}
          initialAt={initialAt}
          active
          onRangeChange={setActiveRange}
          onActiveEntryChange={setActiveSegmentId}
          playSignal={playSignal}
          toolbar={{
            filesOpen: rightOpen,
            onFilesToggle: () => setRightOpen((open) => !open),
            onOpenFilesSheet: () => setSheetOpen(true),
            filesCount: stats.data?.segments,
            showFilesToggle: admin && !!activeChannel,
            narrow,
          }}
          // The workspace layout (rail / player / files + full-width timeline
          // row) is rendered by Playback itself now — the shell hands over
          // the file panel as a slot so it lands in the third column, and
          // Playback owns the timeline band in row 2. No portal, no timing
          // games: one tree, one commit.
          layout={
            narrow
              ? { kind: 'plain' }
              : {
                  kind: 'workspace',
                  rail: (
                    <ChannelsRail
                      channels={channels}
                      activeID={channelID}
                      onSelect={onChannelChange}
                      summaries={summaries}
                      range={activeRange}
                    />
                  ),
                  files:
                    // Collapsed: render NOTHING in column 3 — no placeholder,
                    // no strip, no border. The grid drops to two columns and
                    // the console stretches to the right edge; the search
                    // toolbar's 文件 button is the only way to open the drawer.
                    admin && inlineFileVisible ? (
                      <div
                        data-testid='files-panel'
                        className='flex min-h-0 min-w-0 flex-col overflow-hidden rounded-xl border border-video-border bg-video-surface-raised'
                      >
                        <RecordingFiles
                          controlledChannelId={channelID}
                          initialChannel={channelID}
                          active
                          controlledRange={activeRange ?? undefined}
                          activeSegmentId={activeSegmentId}
                          onClose={() => setRightOpen(false)}
                          onPlaySegment={onPlaySegment}
                        />
                      </div>
                    ) : null,
                }
          }
        />
      )}
      {/* On narrow viewports the file panel rides a side Sheet instead of
          a third grid column. Mount the same panel as the wide layout uses
          so the queries and filter state stay shared via React Query cache. */}
      {narrow && admin && activeChannel && (
        <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
          <SheetContent
            side='right'
            className='flex w-full flex-col gap-0 border-video-border bg-video-surface-raised p-0 text-video-chrome-foreground sm:max-w-sm'
          >
            <SheetHeader className='border-b border-video-border px-4 py-3'>
              <SheetTitle className='text-sm font-semibold text-video-chrome-foreground'>
                录像文件
                {activeChannel
                  ? ` · CH${String(activeChannel.channel_no).padStart(2, '0')} ${activeChannel.channel_name}`
                  : ''}
              </SheetTitle>
            </SheetHeader>
            <div className='flex min-h-0 flex-1 flex-col overflow-hidden'>
              <RecordingFiles
                controlledChannelId={channelID}
                initialChannel={channelID}
                active={sheetOpen}
                controlledRange={activeRange ?? undefined}
                activeSegmentId={activeSegmentId}
                onClose={() => setSheetOpen(false)}
                onPlaySegment={onPlaySegment}
              />
            </div>
          </SheetContent>
        </Sheet>
      )}
    </div>
  )
}

function ChannelsRail({
  channels,
  activeID,
  onSelect,
  summaries,
  range,
}: {
  channels: Schema<'Channel'>[]
  activeID: string
  onSelect: (id: string) => void
  summaries: Map<string, Schema<'ChannelSummary'>>
  range: { start: number; end: number } | null
}) {
  const [search, setSearch] = useState('')
  const filtered = useMemo(() => {
    const needle = search.toLowerCase()
    return channels.filter((channel) => {
      const label = `CH${String(channel.channel_no).padStart(2, '0')} ${channel.channel_name}`
      return label.toLowerCase().includes(needle)
    })
  }, [channels, search])
  return (
    <aside
      data-testid='channels-rail'
      className='flex min-h-0 flex-col gap-2 overflow-hidden rounded-xl border border-video-border bg-video-surface p-2.5 text-video-chrome-foreground'
    >
      <div className='flex items-center justify-between px-1 text-xs font-medium text-video-chrome-foreground/80'>
        <span>通道</span>
        <span className='text-video-chrome-foreground/50'>
          {filtered.length}/{channels.length}
        </span>
      </div>
      <div className='relative'>
        <Search className='pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-video-chrome-foreground/40' />
        <Input
          aria-label='搜索回放通道'
          placeholder='搜索编号或名称'
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          className='h-8 border-video-border bg-white/5 pl-7 text-video-chrome-foreground placeholder:text-video-chrome-foreground/40'
        />
      </div>
      <div className='flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto'>
        {filtered.map((channel) => {
          const label = `CH${String(channel.channel_no).padStart(2, '0')} ${channel.channel_name}`
          const active = channel.id === activeID
          // Status light, from real state: disabled → grey, source error →
          // red, configured + healthy → green. A channel with no applied
          // source revision (`current_revision_id` null — the default
          // placeholder channels) has no stream to be "online", so it stays
          // grey even though its enabled flag is true.
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
                    cls: 'bg-emerald-400 shadow-[0_0_5px_rgba(52,211,153,0.7)]',
                    title: '源在线',
                  }
          return (
            <ChannelRow
              key={channel.id}
              label={label}
              name={channel.channel_name}
              group={channel.channel_group}
              active={active}
              dot={dot}
              onSelect={() => onSelect(channel.id)}
              channelId={channel.id}
              range={range}
            />
          )
        })}
      </div>
    </aside>
  )
}

// One channel row. The per-channel segment count is its own query (the
// playback search window), so a channel with footage in this window shows a
// count badge and channels without any fade — the fastest scan for "which
// cameras have footage here".
function ChannelRow({
  label,
  name,
  group,
  active,
  dot,
  onSelect,
  channelId,
  range,
}: {
  label: string
  name: string
  group: string
  active: boolean
  dot: { cls: string; title: string }
  onSelect: () => void
  channelId: string
  range: { start: number; end: number } | null
}) {
  const path = useMemo(() => {
    if (!range) return ''
    return `/api/v1/recordings/stats?${new URLSearchParams({
      channel_id: channelId,
      start: new Date(range.start).toISOString(),
      end: new Date(range.end).toISOString(),
      limit: '1',
    })}`
  }, [channelId, range])
  const stats = useAPI<Schema<'RecordingStats'>>(path, !!path)
  const segments = stats.data?.segments
  return (
    <button
      type='button'
      onClick={onSelect}
      aria-pressed={active}
      aria-label={label}
      className={cn(
        'flex h-8 items-center gap-2 truncate rounded px-2 text-left text-sm text-video-chrome-foreground hover:bg-white/10',
        active && 'bg-white/15 font-medium text-video-chrome-foreground',
        segments === 0 && 'text-video-chrome-foreground/50'
      )}
    >
      <span
        aria-hidden
        title={dot.title}
        className={cn('inline-block size-1.5 shrink-0 rounded-full', dot.cls)}
      />
      <span className='inline-grid min-w-6 place-items-center rounded bg-white/10 px-1 py-0.5 text-[10px] font-semibold tabular-nums'>
        {label.slice(2, 4)}
      </span>
      <span className='flex-1 truncate'>{name}</span>
      {typeof segments === 'number' && segments > 0 && (
        <span
          title={`当前时段 ${segments} 段录像`}
          className='rounded bg-emerald-400/15 px-1 py-0.5 text-[10px] tabular-nums text-emerald-300'
        >
          {segments > 999 ? '999+' : segments}
        </span>
      )}
      {group && (
        <span
          title={`分组 · ${group}`}
          className='rounded bg-white/10 px-1 py-0.5 text-[10px] text-video-chrome-foreground/60'
        >
          {group}
        </span>
      )}
    </button>
  )
}
