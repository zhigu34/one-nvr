import { useState } from 'react'
import { Playback } from '@/features/playback'
import { RecordingFiles } from '@/features/recording-files'

type Tab = 'playback' | 'files'

// One "recordings" entry with two tabs, as the product decided: browsing and
// playing one recording are different jobs, but they belong to the same
// workspace and must share the channel and the moment being looked at.
//
// The active tab lives in the URL so a deep link or the instant-playback entry
// can land on either one without losing the selection, and switching tabs keeps
// the channel and time context rather than resetting to a default.
export function RecordingsWorkspace({
  initialChannel = '',
  initialAt = '',
  initialTab = '',
}: {
  initialChannel?: string
  initialAt?: string
  initialTab?: string
}) {
  const [tab, setTab] = useState<Tab>(initialTab === 'files' ? 'files' : 'playback')
  return (
    <div className='space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div>
          <h1 className='text-2xl font-semibold tracking-tight'>录像</h1>
          <p className='mt-1 text-sm text-muted-foreground'>
            回放查看与文件管理在同一个工作区，通道与时间在两者之间保留。
          </p>
        </div>
        <div
          role='tablist'
          aria-label='录像视图'
          className='inline-flex rounded-lg border bg-muted/40 p-0.5'
        >
          {([
            { id: 'playback', label: '回放' },
            { id: 'files', label: '录像管理' },
          ] as const).map((entry) => (
            <button
              key={entry.id}
              type='button'
              role='tab'
              aria-selected={tab === entry.id}
              className={`rounded-[6px] px-3 py-1.5 text-sm ${
                tab === entry.id
                  ? 'bg-background font-medium shadow-xs'
                  : 'text-muted-foreground'
              }`}
              onClick={() => setTab(entry.id)}
            >
              {entry.label}
            </button>
          ))}
        </div>
      </div>
      {/* Both panels stay mounted so switching tabs cannot discard an in-flight
          query or the current selection; only visibility changes. */}
      <div role='tabpanel' hidden={tab !== 'playback'}>
        <Playback
          initialChannel={initialChannel}
          initialAt={initialAt}
          embedded={tab === 'playback'}
        />
      </div>
      <div role='tabpanel' hidden={tab !== 'files'}>
        <RecordingFiles
          initialChannel={initialChannel}
          active={tab === 'files'}
        />
      </div>
    </div>
  )
}
