import { useCallback, useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth-store'
import { useAPI } from '@/features/foundation/hooks'
import type { PageData, Schema } from '@/lib/types'
import { Playback } from '@/features/playback'
import { RecordingFiles } from '@/features/recording-files'

type Tab = 'playback' | 'files'

// One "recordings" entry with two tabs, as the product decided: browsing and
// playing one recording are different jobs, but they belong to the same
// workspace and must share the channel and the moment being looked at.
//
// The active tab IS the URL's `tab` search param — not a local copy of it —
// so a deep link lands on the right tab, the back button moves between tabs,
// and switching tabs keeps the channel and time context rather than resetting.
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
  // The tab follows the URL, with one local override: clicking a tab updates
  // immediately and then tries to write the URL. A test harness (or any embed
  // without the /recordings route) still switches, while a real navigation
  // makes the URL the single source of truth for back-button and deep links.
  const [localTab, setLocalTab] = useState<Tab | null>(null)
  const tab: Tab = localTab ?? (initialTab === 'files' ? 'files' : 'playback')
  const channels = useAPI<PageData<Schema<'Channel'>>>('/api/v1/channels?limit=100')
  // The management tab only exists for people who can see anything in it:
  // the page is admin-only, so a viewer is never shown a tab that would
  // render one sentence of refusal.
  const showFilesTab = useMemo(
    () =>
      admin &&
      (channels.data?.items || []).some((channel) =>
        channel.permissions.includes('playback')
      ),
    [admin, channels.data]
  )
  const setTab = useCallback(
    (next: Tab) => {
      setLocalTab(next)
      // The route owns the search shape (channel/at/tab all default to ''); the
      // channel and moment ride along so switching tabs never resets them.
      void navigate({
        to: '/recordings',
        search: {
          channel: initialChannel,
          at: initialAt,
          tab: next === 'files' ? 'files' : '',
        },
      }).catch(() => {
        // No /recordings route in this router (tests, embedded use): the local
        // override above is then the state, and that is fine.
      })
    },
    [navigate, initialChannel, initialAt]
  )
  return (
    <div className='flex min-h-[calc(100vh-6rem)] flex-col gap-3'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <h1 className='text-lg font-semibold tracking-tight'>录像</h1>
        <div
          role='tablist'
          aria-label='录像视图'
          className='inline-flex rounded-lg border bg-muted/40 p-0.5'
        >
          {([
            { id: 'playback', label: '回放' },
            ...(showFilesTab ? [{ id: 'files', label: '录像管理' }] : []),
          ] as { id: Tab; label: string }[]).map((entry) => (
            <button
              key={entry.id}
              type='button'
              role='tab'
              aria-selected={tab === entry.id}
              className={`rounded-[6px] px-3 py-1.5 text-sm transition-colors ${
                tab === entry.id
                  ? 'bg-background font-medium shadow-xs'
                  : 'text-muted-foreground hover:text-foreground'
              }`}
              onClick={() => setTab(entry.id)}
            >
              {entry.label}
            </button>
          ))}
        </div>
      </div>
      {/* Both panels stay mounted so switching tabs cannot discard an in-flight
          query or the current selection; only visibility changes. The playback
          side additionally gates its heavy index fetch on being the active tab. */}
      <div
        role='tabpanel'
        hidden={tab !== 'playback'}
        className={tab === 'playback' ? 'flex min-h-0 flex-1 flex-col' : undefined}
      >
        <Playback
          initialChannel={initialChannel}
          initialAt={initialAt}
          active={tab === 'playback'}
        />
      </div>
      {showFilesTab && (
        <div role='tabpanel' hidden={tab !== 'files'}>
          <RecordingFiles
            initialChannel={initialChannel}
            active={tab === 'files'}
          />
        </div>
      )}
    </div>
  )
}
