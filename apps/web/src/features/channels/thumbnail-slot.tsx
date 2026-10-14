import { Cctv } from 'lucide-react'
import { cn } from '@/lib/utils'

// A 16:9 mini preview slot. Channel-level snapshots have no endpoint yet (the
// only snapshot route is per-event under Frigate), so `src` is reserved for
// the day one lands; today every row renders its state glyph.
export function ThumbnailSlot({
  src,
  online,
  disabled,
  unconfigured,
  label,
}: {
  src?: string
  online: boolean
  disabled: boolean
  unconfigured: boolean
  label: string
}) {
  return (
    <div
      aria-hidden
      title={label}
      data-testid='channel-thumbnail'
      data-state={unconfigured ? 'unconfigured' : disabled ? 'disabled' : online ? 'online' : 'offline'}
      className='flex h-8 w-14 shrink-0 items-center justify-center overflow-hidden rounded border border-zinc-800 bg-zinc-900'
    >
      {src && !unconfigured ? (
        <img
          src={src}
          alt=''
          className={cn(
            'h-full w-full object-cover',
            disabled && 'grayscale opacity-60'
          )}
        />
      ) : unconfigured ? (
        // The faintest possible slot: a dashed outline and a dash, so an
        // empty channel reads as "nothing here" without drawing the eye.
        <span className='grid h-full w-full place-items-center border border-dashed border-zinc-800 text-[10px] leading-none text-zinc-700'>
          —
        </span>
      ) : (
        <Cctv
          className={cn(
            'size-5',
            disabled
              ? 'text-zinc-600'
              : online
                ? 'text-emerald-500 drop-shadow-[0_0_3px_rgba(16,185,129,0.6)]'
                : 'text-zinc-500'
          )}
        />
      )}
    </div>
  )
}
