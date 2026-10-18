import { Archive, HardDrive } from 'lucide-react'
import { cn } from '@/lib/utils'
import { StoragePools } from '@/features/storage-pools'
import { ArchiveTargets } from '@/features/archive-targets'

/**
 * Storage is one surface with two tabs: where recordings are written, and where
 * copies are archived.
 *
 * The two pages answer one question between them — "will this recording survive
 * and where is it?" — and answering it used to require switching between two
 * first-level menu entries. They are also the same kind of thing: site
 * infrastructure configured by an administrator, as opposed to business
 * configuration like channels and policies.
 *
 * Both panels stay mounted, and the inactive one is hidden rather than
 * unmounted, following the recordings workspace. A pool check or a form being
 * filled in is not lost by looking at the other tab.
 */
const TABS = [
  {
    id: 'pools',
    label: '存储池',
    hint: '录像写到哪里',
    icon: HardDrive,
  },
  {
    id: 'archive',
    label: '归档目标',
    hint: '副本上传到哪里',
    icon: Archive,
  },
] as const

export type StorageTab = (typeof TABS)[number]['id']

export function StorageWorkspace({
  tab,
  onTabChange,
}: {
  /** Which panel is showing. Owned by the route so the address bar tracks it. */
  tab: StorageTab
  onTabChange: (tab: StorageTab) => void
}) {
  return (
    <>
      <div className='mb-5 flex flex-wrap items-center gap-2'>
        {TABS.map((item) => {
          const Icon = item.icon
          const active = tab === item.id
          return (
            <button
              key={item.id}
              type='button'
              aria-pressed={active}
              onClick={() => onTabChange(item.id)}
              className={cn(
                'flex items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-colors',
                active
                  ? 'border-border bg-accent text-accent-foreground'
                  : 'border-transparent text-muted-foreground hover:bg-accent/50'
              )}
            >
              <Icon aria-hidden className='size-4' />
              <span>{item.label}</span>
              <span className='hidden text-xs text-muted-foreground sm:inline'>
                {item.hint}
              </span>
            </button>
          )
        })}
      </div>
      {/* Hidden rather than unmounted: an open form and its scroll position
          survive a glance at the other tab. */}
      <div
        data-storage-panel='pools'
        className={tab === 'pools' ? '' : 'hidden'}
        aria-hidden={tab !== 'pools'}
      >
        <StoragePools />
      </div>
      <div
        data-storage-panel='archive'
        className={tab === 'archive' ? '' : 'hidden'}
        aria-hidden={tab !== 'archive'}
      >
        <ArchiveTargets />
      </div>
    </>
  )
}
