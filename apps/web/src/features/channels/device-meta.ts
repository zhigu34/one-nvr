/**
 * Camera device metadata: brand presets, RTSP path templates, and the inference
 * from an ONVIF probe.
 *
 * Kept out of the form so the rules are testable: which vendor gets which path,
 * what a probe result implies for the camera type, and when a field counts as
 * "recognised by the device" rather than typed by the operator.
 */

export const BRAND_PRESETS = [
  '海康威视',
  '大华',
  '宇视',
  'TP-LINK',
  '萤石',
  '乐橙',
  'Axis',
] as const

export const CAMERA_TYPES = [
  { value: 'bullet', label: '枪机' },
  { value: 'dome', label: '半球' },
  { value: 'ptz', label: '球机 / 云台 PTZ' },
  { value: 'fisheye', label: '全景 / 鱼眼' },
  { value: 'multi', label: '多目相机' },
  { value: 'nvr', label: 'NVR 通道' },
] as const

export type CameraType = (typeof CAMERA_TYPES)[number]['value'] | ''

export function cameraTypeLabel(value: string | undefined): string {
  return CAMERA_TYPES.find((entry) => entry.value === value)?.label ?? ''
}

/**
 * Path templates per brand.
 *
 * These are the vendor defaults an operator would otherwise look up: Hikvision
 * exposes channel 1 main/sub as 101/102, Dahua as subtype=0/1, and so on. A
 * template is only a starting point — every field stays editable, because the
 * same brand ships models that differ (a Dahua NVR channel 3 is 301/302).
 */
export interface PathTemplate {
  main: string
  sub: string
  /** Short explanation shown next to the button. */
  hint: string
}

const TEMPLATES: Record<string, PathTemplate> = {
  海康威视: {
    main: '/Streaming/Channels/101',
    sub: '/Streaming/Channels/102',
    hint: '通道 1 主流/子流',
  },
  萤石: {
    main: '/Streaming/Channels/101',
    sub: '/Streaming/Channels/102',
    hint: '与海康同源协议',
  },
  大华: {
    main: '/cam/realmonitor?channel=1&subtype=0&unicast=true&proto=Onvif',
    sub: '/cam/realmonitor?channel=1&subtype=1&unicast=true&proto=Onvif',
    hint: 'channel=1, subtype 0/1',
  },
  乐橙: {
    main: '/cam/realmonitor?channel=1&subtype=0&unicast=true&proto=Onvif',
    sub: '/cam/realmonitor?channel=1&subtype=1&unicast=true&proto=Onvif',
    hint: '与大华同源协议',
  },
  宇视: { main: '/media/video1', sub: '/media/video2', hint: 'video1 / video2' },
  'TP-LINK': { main: '/stream1', sub: '/stream2', hint: 'stream1 / stream2' },
  Axis: {
    main: '/axis-media/media.amp',
    sub: '/axis-media/media.amp?resolution=640x480',
    hint: 'Axis 单一媒体入口',
  },
}

export function pathTemplate(brand: string | undefined): PathTemplate | null {
  if (!brand) return null
  return TEMPLATES[brand.trim()] ?? null
}

/**
 * inferCameraType maps an ONVIF probe onto the type list.
 *
 * The only thing the protocol actually reports is a PTZ node; everything else is
 * a guess this function refuses to make. A dome and a bullet look identical over
 * ONVIF, so leaving the field empty is more honest than picking one — the
 * operator can fill it in, and an empty field says "not recorded" whereas a wrong
 * one says something false.
 */
export function inferCameraType(ptz: boolean): CameraType {
  return ptz ? 'ptz' : ''
}

/**
 * inferredBrand keeps the device's own manufacturer string.
 *
 * It is not snapped to a preset: the presets are a convenience list, and a device
 * reporting "HIKVISION" or "Hangzhou Hikvision" is neither the preset string nor
 * wrong. Matching happens only to highlight that a preset exists.
 */
export function inferredBrand(manufacturer: string | undefined): string {
  const value = (manufacturer ?? '').trim()
  if (!value) return ''
  return value.length > 64 ? value.slice(0, 64) : value
}

/** suggestedBrandPreset finds the preset a manufacturer string corresponds to. */
export function suggestedBrandPreset(manufacturer: string | undefined): string {
  const value = (manufacturer ?? '').trim().toLowerCase()
  if (!value) return ''
  for (const preset of BRAND_PRESETS) {
    if (value.includes(preset.toLowerCase())) return preset
  }
  // Vendor names appear in several spellings across firmware versions.
  if (value.includes('hikvision')) return '海康威视'
  if (value.includes('dahua') || value.includes('lechange')) return '大华'
  if (value.includes('uniview') || value.includes('unv')) return '宇视'
  if (value.includes('tp-link') || value.includes('tplink')) return 'TP-LINK'
  if (value.includes('ezviz')) return '萤石'
  if (value.includes('axis')) return 'Axis'
  return ''
}

/** inferredModel trims a reported model to the column width the API accepts. */
export function inferredModel(model: string | undefined): string {
  const value = (model ?? '').trim()
  if (!value) return ''
  return value.length > 128 ? value.slice(0, 128) : value
}

/**
 * chooseDefaultStreams decides which profile is the main stream.
 *
 * Sorting by resolution looked reasonable and was wrong. A Dahua camera returns
 * Profile000 (subtype=0, the main stream) with no resolution in its GetProfiles
 * response and Profile001 (subtype=1, the sub stream) at 704×576, so "largest
 * first" picked the sub stream as main. The paths then read subtype=1 for main
 * and subtype=0 for sub — exactly reversed.
 *
 * The order of evidence is:
 *
 *  1. The URI. `subtype=0`, `/101`, `/main`, `stream1` are main-stream markers;
 *     their siblings are sub. This is what the device actually serves.
 *  2. The device's own profile order. ONVIF has no "this is the main stream"
 *     flag, but every vendor returns the main profile first.
 *  3. Resolution, and only as a tie-break between profiles whose URI says
 *     nothing — never as a reason to demote a profile to sub.
 *
 * A profile with unparsed dimensions is therefore not "less important": it is
 * simply a profile whose size the device did not report.
 */
export interface StreamLike {
  token: string
  name?: string
  path?: string
  width?: number
  height?: number
}

const MAIN_MARKERS = [
  /(?:^|[?&/])subtype=0(?:&|$)/i,
  /\/10*1(?:\?|$|\/)/,
  /\/(?:main|stream1|video1)(?:[?/]|$)/i,
]
const SUB_MARKERS = [
  /(?:^|[?&/])subtype=[1-9]/i,
  /\/10*[2-9](?:\?|$|\/)/,
  /\/(?:sub|stream[2-9]|video[2-9])(?:[?/]|$)/i,
]

/** streamRole reads the URI, which is the only per-profile fact the device serves. */
export function streamRole(stream: StreamLike): 'main' | 'sub' | '' {
  const path = stream.path ?? ''
  if (!path) return ''
  for (const marker of SUB_MARKERS) if (marker.test(path)) return 'sub'
  for (const marker of MAIN_MARKERS) if (marker.test(path)) return 'main'
  return ''
}

/**
 * chooseDefaultStreams returns [main, sub], either of which may be null.
 *
 * `streams` is in the device's order: ONVIF has no main flag, but vendors return
 * the main profile first, so the order is the fallback evidence after the URI.
 */
export function chooseDefaultStreams<T extends StreamLike>(
  streams: T[]
): { main: T | null; sub: T | null } {
  if (streams.length === 0) return { main: null, sub: null }

  const byRole: Record<'main' | 'sub', T[]> = { main: [], sub: [] }
  const unmarked: T[] = []
  for (const stream of streams) {
    const role = streamRole(stream)
    if (role === '') unmarked.push(stream)
    else byRole[role].push(stream)
  }

  // The URI decides when it can. Multiple candidates in one role keep device
  // order; resolution only breaks the tie when a URI left it open.
  const main =
    byRole.main[0] ??
    unmarked[0] ??
    byRole.sub[0] ??
    streams[0]
  const sub =
    streams.find((stream) => stream.token !== main.token) ?? null
  return { main, sub }
}
