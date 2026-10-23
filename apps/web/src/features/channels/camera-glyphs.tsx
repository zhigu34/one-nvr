import type { SVGProps } from 'react'

/**
 * Camera form-factor glyphs, drawn for this product.
 *
 * The first version of this column mapped types onto whatever generic icons the
 * icon library happened to ship — a stacked-server glyph for an NVR channel, a
 * three-way arrow for a PTZ. Both were technically "a distinct icon per type"
 * and both failed the only test that matters: an operator cannot tell what kind
 * of camera a row is by looking at them. These are drawn as the objects
 * themselves, in the same 24px stroke style as the rest of the interface.
 *
 * Every path is intentionally a single-colour outline in `currentColor` so the
 * state tints (online / offline / disabled) keep working unchanged.
 */
type GlyphProps = SVGProps<SVGSVGElement>

const base = {
  width: 24,
  height: 24,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.75,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
}

/** 枪机：机身 + 遮阳罩 + 支架，指向左前方。 */
export function BulletCamera(props: GlyphProps) {
  return (
    <svg {...base} {...props}>
      {/* A lens ring at the front is what separates a bullet from the NVR's
          flat panel at this size; without it the two outlines read alike. */}
      <path d='M3 10.5 14 6.5a1 1 0 0 1 1.3.6l1.9 5.2a1 1 0 0 1-.6 1.3L5.6 17.6a1 1 0 0 1-1.3-.6l-1.9-5.2A1 1 0 0 1 3 10.5Z' />
      <ellipse cx='5.9' cy='13.8' rx='2.1' ry='1.6' transform='rotate(-20 5.9 13.8)' />
      <path d='M16.6 8.2 19.4 7.2' />
      <path d='M9.5 18.2v1.6a1 1 0 0 0 1 1h1.3a1 1 0 0 0 1-1v-.9' />
    </svg>
  )
}

/** 半球：罩在底座上的半圆，正面一条分割线表示球罩。 */
export function DomeCamera(props: GlyphProps) {
  return (
    <svg {...base} {...props}>
      <path d='M4 15a8 8 0 0 1 16 0' />
      <path d='M2.5 15h19' />
      <path d='M2.5 15v2a1 1 0 0 0 1 1h17a1 1 0 0 0 1-1v-2' />
      <path d='M9 9.6a5.2 5.2 0 0 1 6 0' />
    </svg>
  )
}

/** 球机：悬臂吊装的球形机身，能看出它可以转向。 */
export function PTZCamera(props: GlyphProps) {
  return (
    <svg {...base} {...props}>
      <path d='M4 4h6' />
      <path d='M7 4v3' />
      <path d='M7 7v1.5' />
      <circle cx='12' cy='14' r='5.2' />
      <path d='M7 8.5h5a5.2 5.2 0 0 1 0 2.3' />
      <circle cx='12' cy='14' r='1.6' />
    </svg>
  )
}

/** 鱼眼 / 全景：广角镜头，同心圆。 */
export function FisheyeCamera(props: GlyphProps) {
  return (
    <svg {...base} {...props}>
      <circle cx='12' cy='12' r='9' />
      <circle cx='12' cy='12' r='5.4' />
      <circle cx='12' cy='12' r='1.8' />
      <path d='M12 3v2.2M12 18.8V21M3 12h2.2M18.8 12H21' />
    </svg>
  )
}

/** 多目：一个机身里两只镜头，正是多目相机的外观。 */
export function MultiCamera(props: GlyphProps) {
  return (
    <svg {...base} {...props}>
      <rect x='2.5' y='7' width='19' height='10' rx='2' />
      <circle cx='8.5' cy='12' r='2.6' />
      <circle cx='15.5' cy='12' r='2.6' />
      <path d='M12 7V5.5' />
      <path d='M7 17v2M17 17v2' />
    </svg>
  )
}

/** NVR 通道：一台放在机架上的录像机，前面板带指示灯。 */
export function RecorderChannel(props: GlyphProps) {
  return (
    <svg {...base} {...props}>
      <rect x='2.5' y='6.5' width='19' height='11' rx='1.4' />
      <path d='M2.5 9.5h19' />
      <path d='M16.5 9.5v8' />
      <circle cx='5.6' cy='8' r='0.85' fill='currentColor' stroke='none' />
      <circle cx='8.4' cy='8' r='0.85' fill='currentColor' stroke='none' />
      <path d='M5 12.5h7M5 15h4.5' />
    </svg>
  )
}

/** 未记录类型：一台通用摄像机，不假装知道它的形态。 */
export function UnknownCamera(props: GlyphProps) {
  return (
    <svg {...base} {...props}>
      <rect x='2.5' y='8' width='13' height='9' rx='1.6' />
      <path d='M15.5 12.2 21 9.6v6.8l-5.5-2.6' />
      <path d='M6 17v2.2h6V17' />
    </svg>
  )
}

/**
 * The glyph for one recorded form factor.
 *
 * An unrecorded type gets the generic body: this column says "there is a video
 * source here", and "the shape is unknown" is not the same as "something is
 * wrong with it".
 */
export function CameraTypeGlyph({
  cameraType,
  ...props
}: GlyphProps & { cameraType?: string }) {
  switch (cameraType) {
    case 'bullet':
      return <BulletCamera {...props} />
    case 'dome':
      return <DomeCamera {...props} />
    case 'ptz':
      return <PTZCamera {...props} />
    case 'fisheye':
      return <FisheyeCamera {...props} />
    case 'multi':
      return <MultiCamera {...props} />
    case 'nvr':
      return <RecorderChannel {...props} />
    default:
      return <UnknownCamera {...props} />
  }
}
