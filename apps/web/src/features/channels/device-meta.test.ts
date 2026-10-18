import { describe, expect, test } from 'vitest'
import {
  BRAND_PRESETS,
  CAMERA_TYPES,
  cameraTypeLabel,
  chooseDefaultStreams,
  inferredBrand,
  inferredModel,
  inferCameraType,
  pathTemplate,
  suggestedBrandPreset,
} from './device-meta'

describe('pathTemplate', () => {
  // The vendor defaults an operator would otherwise look up. Channel 1 only:
  // every template stays editable because the same brand ships models whose
  // numbering differs (a Dahua NVR's channel 3 is 301/302).
  test('gives the common vendors their default paths', () => {
    const hik = pathTemplate('海康威视')
    expect(hik?.main).toBe('/Streaming/Channels/101')
    expect(hik?.sub).toBe('/Streaming/Channels/102')

    const dahua = pathTemplate('大华')
    expect(dahua?.main).toContain('subtype=0')
    expect(dahua?.sub).toContain('subtype=1')

    expect(pathTemplate('TP-LINK')?.main).toBe('/stream1')
    expect(pathTemplate('Axis')?.main).toBe('/axis-media/media.amp')
  })

  // A brand with no known template must not silently produce a wrong path.
  test('returns nothing for an unknown or empty brand', () => {
    expect(pathTemplate('')).toBeNull()
    expect(pathTemplate(undefined)).toBeNull()
    expect(pathTemplate('某个没见过的牌子')).toBeNull()
  })
})

describe('inferCameraType', () => {
  // The protocol reports a PTZ node and nothing else. A dome and a bullet are
  // indistinguishable over ONVIF, so guessing one would be inventing a fact;
  // an empty field says "not recorded".
  test('infers PTZ from the PTZ node', () => {
    expect(inferCameraType(true)).toBe('ptz')
  })

  test('leaves the type unrecorded without a PTZ node', () => {
    expect(inferCameraType(false)).toBe('')
  })
})

describe('inferredBrand', () => {
  // The device's own spelling is kept rather than snapped to a preset: a camera
  // reporting "HIKVISION" is neither the preset string nor wrong.
  test('keeps the manufacturer string as reported', () => {
    expect(inferredBrand('HIKVISION')).toBe('HIKVISION')
    expect(inferredBrand('  Dahua Technology  ')).toBe('Dahua Technology')
  })

  test('returns empty for a missing manufacturer', () => {
    expect(inferredBrand(undefined)).toBe('')
    expect(inferredBrand('')).toBe('')
  })

  test('truncates to the stored column width', () => {
    expect(inferredBrand('x'.repeat(100))).toHaveLength(64)
  })
})

describe('suggestedBrandPreset', () => {
  // Firmware spells the same vendor several ways; the preset is offered as a
  // suggestion, and the reported string is kept if no preset matches.
  test('matches the preset for known vendor spellings', () => {
    expect(suggestedBrandPreset('HIKVISION')).toBe('海康威视')
    expect(suggestedBrandPreset('Hangzhou Hikvision Digital')).toBe('海康威视')
    expect(suggestedBrandPreset('Dahua Technology')).toBe('大华')
    expect(suggestedBrandPreset('Uniview')).toBe('宇视')
    expect(suggestedBrandPreset('AXIS COMMUNICATIONS')).toBe('Axis')
    expect(suggestedBrandPreset('TP-LINK')).toBe('TP-LINK')
  })

  test('returns empty when no preset matches', () => {
    expect(suggestedBrandPreset('Acme Optics')).toBe('')
    expect(suggestedBrandPreset(undefined)).toBe('')
  })
})

describe('inferredModel', () => {
  test('keeps and trims the reported model', () => {
    expect(inferredModel(' FX-9000 ')).toBe('FX-9000')
    expect(inferredModel(undefined)).toBe('')
  })

  test('truncates to the stored column width', () => {
    expect(inferredModel('m'.repeat(300))).toHaveLength(128)
  })
})

describe('cameraTypeLabel', () => {
  test('names every selectable type', () => {
    for (const entry of CAMERA_TYPES) {
      expect(cameraTypeLabel(entry.value)).toBe(entry.label)
    }
  })

  test('returns empty for an unrecorded type', () => {
    expect(cameraTypeLabel('')).toBe('')
    expect(cameraTypeLabel(undefined)).toBe('')
  })
})

describe('presets', () => {
  test('brand presets cover the vendors this product is deployed with', () => {
    for (const brand of ['海康威视', '大华', '宇视', 'TP-LINK', 'Axis']) {
      expect(BRAND_PRESETS).toContain(brand)
    }
  })
})

describe('chooseDefaultStreams', () => {
  // The exact case that inverted this site's Dahua camera: Profile000 is
  // subtype=0 (main) but reports no resolution, Profile001 is subtype=1 (sub)
  // at 704×576. Sorting by resolution picked the sub stream as main.
  test('follows the URI when the main profile reports no size', () => {
    const streams = [
      {
        token: 'Profile000',
        name: 'Profile000',
        path: '/cam/realmonitor?channel=1&subtype=0&unicast=true&proto=Onvif',
      },
      {
        token: 'Profile001',
        name: 'Profile001',
        path: '/cam/realmonitor?channel=1&subtype=1&unicast=true&proto=Onvif',
        width: 704,
        height: 576,
      },
    ]
    const { main, sub } = chooseDefaultStreams(streams)
    expect(main?.token).toBe('Profile000')
    expect(sub?.token).toBe('Profile001')
  })

  test('recognises Hikvision channel numbering', () => {
    const { main, sub } = chooseDefaultStreams([
      { token: 'a', path: '/Streaming/Channels/102' },
      { token: 'b', path: '/Streaming/Channels/101' },
    ])
    expect(main?.token).toBe('b')
    expect(sub?.token).toBe('a')
  })

  test('recognises main and sub path words', () => {
    const { main, sub } = chooseDefaultStreams([
      { token: 'a', path: '/ch1/sub' },
      { token: 'b', path: '/ch1/main' },
    ])
    expect(main?.token).toBe('b')
    expect(sub?.token).toBe('a')
  })

  // ONVIF has no main-stream flag, so the device's order is the fallback when
  // the paths carry no marker. That order is what a vendor returns.
  test('falls back to the device order when the URI says nothing', () => {
    const { main, sub } = chooseDefaultStreams([
      { token: 'first', path: '/stream' },
      { token: 'second', path: '/stream2x' },
    ])
    expect(main?.token).toBe('first')
    expect(sub?.token).toBe('second')
  })

  // A single-profile device still yields a main stream and no sub.
  test('handles a single profile', () => {
    const { main, sub } = chooseDefaultStreams([{ token: 'only', path: '/media' }])
    expect(main?.token).toBe('only')
    expect(sub).toBeNull()
  })

  test('handles no profiles', () => {
    expect(chooseDefaultStreams([])).toEqual({ main: null, sub: null })
  })
})
