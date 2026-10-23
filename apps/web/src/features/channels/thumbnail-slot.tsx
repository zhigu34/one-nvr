import {
  Aperture,
  Camera,
  Cctv,
  Grid2x2,
  Move3d,
  Server,
  type LucideIcon,
} from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * The glyph for one camera form factor.
 *
 * Every channel used to show the same Cctv outline, so a wall of sixteen rows
 * said "camera" sixteen times and never which kind. The type is recorded on the
 * channel (set by hand or inferred from ONVIF's PTZ node), so the list can show
 * it: a bullet pointing at the street, a dome on the ceiling, an aperture for a
 * fisheye. An unrecorded type keeps the generic outline rather than guessing.
 */
const CAMERA_GLYPHS: Record<string, LucideIcon> = {
  bullet: Cctv,
  dome: Camera,
  ptz: Move3d,
  fisheye: Aperture,
  multi: Grid2x2,
  nvr: Server,
}

// A 16:9 mini preview slot. Channel-level snapshots have no endpoint yet (the
// only snapshot route is per-event under Frigate), so `src` is reserved for
// the day one lands; today every row renders its state glyph.
export function ThumbnailSlot({
  src,
  online,
  disabled,
  unconfigured,
  cameraType,
  label,
}: {
  src?: string
  online: boolean
  disabled: boolean
  unconfigured: boolean
  /** The channel's recorded form factor; absent means "not recorded". */
  cameraType?: string
  label: string
}) {
  const Glyph = CAMERA_GLYPHS[cameraType ?? ''] ?? Cctv
  return (
    <div
      aria-hidden
      title={cameraType ? `${label} · ${cameraType}` : label}
      data-testid='channel-thumbnail'
      data-state={unconfigured ? 'unconfigured' : disabled ? 'disabled' : online ? 'online' : 'offline'}
      data-camera-type={cameraType || 'unknown'}
      className='flex h-8 w-14 shrink-0 items-center justify-center overflow-hidden rounded border border-zinc-200 bg-zinc-100 text-zinc-400 dark:border-zinc-800 dark:bg-zinc-900'
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
        <span className='grid h-full w-full place-items-center border border-dashed border-zinc-300 text-[10px] leading-none text-zinc-400 dark:border-zinc-800 dark:text-zinc-700'>
          —
        </span>
      ) : (
        <Glyph
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
