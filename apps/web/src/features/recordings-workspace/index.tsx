import { useMemo, useState, useSyncExternalStore } from 'react'
import { Filter, PanelRightClose, PanelRightOpen, Search } from 'lucide-react'
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
  const [rightOpen, setRightOpen] = useState(true)
  const [sheetOpen, setSheetOpen] = useState(false)
  const narrow = useNarrowViewport()
  // Resizing from wide → narrow leaves the rail half-collapsed; reset to
  // expanded by default on the bigger layout, leave the Sheet closed on the
  // smaller one. This avoids a flash of "panel showing partly" on resize.
  const inlineFileVisible = !narrow && rightOpen
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
  const zone = site.data?.timezone || ''
  // Capture the timestamp once per mount; the stats window is "last 7 days"
  // from page load, and the path stays stable across re-renders inside the
  // same mount.
  const [statsNow] = useState(() => Date.now())
  const statsPath = useMemo(() => {
    if (!activeChannel) return ''
    return `/api/v1/recordings/stats?${new URLSearchParams({
      channel_id: activeChannel.id,
      start: new Date(statsNow - 7 * 24 * 3600_000).toISOString(),
      end: new Date(statsNow).toISOString(),
      limit: '100',
    })}`
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeChannel?.id])
  const stats = useAPI<Schema<'RecordingStats'>>(
    statsPath,
    rightOpen && admin && !!activeChannel
  )
  return (
    <div
      data-testid='recordings-workspace'
      className='flex h-[calc(100vh-4rem)] flex-col gap-3 overflow-hidden bg-video-surface text-video-chrome-foreground'
    >
      <QueryState
        pending={channelsQuery.isPending || site.isPending}
        error={channelsQuery.error || site.error}
        retry={() => {
          void channelsQuery.refetch()
          void site.refetch()
        }}
      />
      <WorkspaceHeader
        zone={zone}
        channel={activeChannel}
        segmentCount={stats.data?.segments}
        rightOpen={rightOpen}
        onToggleRight={() => setRightOpen((open) => !open)}
        onOpenSheet={() => setSheetOpen(true)}
        showRightToggle={admin && !!activeChannel}
        sheetTriggerLabel={narrow ? `查看文件（${stats.data?.segments ?? '…'} 段）` : undefined}
        narrow={narrow}
      />
      {!channels.length && channelsQuery.data && (
        <Empty>没有可回放的通道，请联系管理员分配录像回放权限。</Empty>
      )}
      {!!channels.length && (
        <div
          className='grid min-h-0 flex-1 gap-3 overflow-hidden'
          style={{
            gridTemplateColumns: narrow
              ? '192px minmax(0, 1fr)'
              : rightOpen
                ? '192px minmax(0, 1fr) 320px'
                : '192px minmax(0, 1fr) 56px',
            // An explicit row fraction lets each cell stretch to the grid's
            // own (flex-grown) height, not just the tallest content's. Without
            // this the central Playback column collapses to its content and
            // its OSD overlay ends up stacked above the status strip instead
            // of floating at the bottom of the video.
            gridTemplateRows: 'minmax(0, 1fr)',
          }}
        >
          <ChannelsRail
            channels={channels}
            activeID={channelID}
            onSelect={onChannelChange}
          />
          <div className='flex min-h-0 min-w-0 flex-col overflow-hidden rounded-xl border border-video-border bg-black'>
            <Playback
              providedChannels={channels}
              controlledChannelId={channelID}
              onChannelChange={onChannelChange}
              hideChannelList
              initialChannel={channelID}
              initialAt={initialAt}
              active
            />
          </div>
          {!narrow && admin ? (
            inlineFileVisible ? (
              <div
                data-testid='files-panel'
                className='flex min-h-0 min-w-0 flex-col overflow-hidden rounded-xl border border-video-border bg-video-surface-raised'
              >
                <RecordingFiles
                  controlledChannelId={channelID}
                  initialChannel={channelID}
                  active
                />
              </div>
            ) : (
              <button
                type='button'
                aria-label='展开文件面板'
                title='展开文件面板'
                onClick={() => setRightOpen(true)}
                className='flex w-full flex-col items-center justify-center gap-2 rounded-xl border border-video-border bg-video-surface-raised text-video-chrome-foreground/70 hover:text-video-chrome-foreground'
              >
                <PanelRightOpen className='size-5' />
                <span className='writing-mode-vertical text-xs'>
                  文件 {stats.data?.segments ?? '…'} 段
                </span>
              </button>
            )
          ) : null}
        </div>
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
              />
            </div>
          </SheetContent>
        </Sheet>
      )}
    </div>
  )
}

function WorkspaceHeader({
  zone,
  channel,
  segmentCount,
  rightOpen,
  onToggleRight,
  onOpenSheet,
  showRightToggle,
  sheetTriggerLabel,
  narrow,
}: {
  zone: string
  channel?: Schema<'Channel'>
  segmentCount?: number
  rightOpen: boolean
  onToggleRight: () => void
  onOpenSheet: () => void
  showRightToggle: boolean
  sheetTriggerLabel?: string
  narrow: boolean
}) {
  return (
    <header className='flex flex-wrap items-center gap-3 border-b border-video-border px-1 pb-2'>
      <h1 className='text-lg font-semibold tracking-tight'>录像</h1>
      <span className='text-video-chrome-foreground/40'>·</span>
      <span className='text-sm text-video-chrome-foreground/70'>
        {channel
          ? `CH${String(channel.channel_no).padStart(2, '0')} ${channel.channel_name}`
          : '选择通道'}
      </span>
      <span className='text-video-chrome-foreground/30'>·</span>
      <span className='text-xs text-video-chrome-foreground/60'>
        {zone || '站点时区…'}
      </span>
      {showRightToggle && (
        <div className='ml-auto flex items-center gap-2'>
          <span className='inline-flex items-center gap-1.5 rounded-md border border-video-border bg-video-surface-raised px-2 py-1 text-xs text-video-chrome-foreground/70'>
            <Filter className='size-3' />
            文件 {segmentCount ?? '…'} 段
          </span>
          {narrow ? (
            <button
              type='button'
              aria-label='查看文件'
              title='查看文件'
              onClick={onOpenSheet}
              className='inline-flex h-8 items-center gap-1.5 rounded-md border border-video-border bg-video-surface-raised px-2 text-xs text-video-chrome-foreground hover:bg-white/10'
            >
              <PanelRightOpen className='size-4' />
              {sheetTriggerLabel ?? '文件'}
            </button>
          ) : (
            <button
              type='button'
              aria-label={rightOpen ? '收起文件面板' : '展开文件面板'}
              title={rightOpen ? '收起文件面板' : '展开文件面板'}
              onClick={onToggleRight}
              className='inline-flex h-8 items-center gap-1.5 rounded-md border border-video-border bg-video-surface-raised px-2 text-xs text-video-chrome-foreground hover:bg-white/10'
            >
              {rightOpen ? (
                <PanelRightClose className='size-4' />
              ) : (
                <PanelRightOpen className='size-4' />
              )}
              {rightOpen ? '收起文件' : '文件'}
            </button>
          )}
        </div>
      )}
    </header>
  )
}

function ChannelsRail({
  channels,
  activeID,
  onSelect,
}: {
  channels: Schema<'Channel'>[]
  activeID: string
  onSelect: (id: string) => void
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
          return (
            <button
              key={channel.id}
              type='button'
              onClick={() => onSelect(channel.id)}
              aria-pressed={active}
              aria-label={label}
              className={cn(
                'flex h-8 items-center gap-2 truncate rounded px-2 text-left text-sm text-video-chrome-foreground hover:bg-white/10',
                active && 'bg-white/15 font-medium text-video-chrome-foreground'
              )}
            >
              <span className='inline-grid size-5 place-items-center rounded bg-white/10 text-[10px] font-semibold tabular-nums'>
                {String(channel.channel_no).padStart(2, '0')}
              </span>
              <span className='flex-1 truncate'>{channel.channel_name}</span>
            </button>
          )
        })}
      </div>
    </aside>
  )
}
