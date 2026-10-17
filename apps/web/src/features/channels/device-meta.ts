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
