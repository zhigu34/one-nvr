import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page } from 'vitest/browser'
import type { Schema } from '@/lib/types'
import { ChannelReliability } from './reliability'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children }: { children: React.ReactNode }) => <span>{children}</span>,
}))
afterEach(() => {
  vi.clearAllMocks()
})

const channelID = '00000000-0000-4000-8000-000000000001'

function rate(over: Partial<Schema<'ReliabilityRate'>> = {}): Schema<'ReliabilityRate'> {
  return { status: 'ok', numerator_ms: 40_000, denominator_ms: 50_000, unjudgeable_ms: 0, ...over }
}

function metrics(kind: Schema<'ReliabilityMetrics'>['kind']): Schema<'ReliabilityMetrics'> {
  return {
    kind,
    window: { from: '2026-10-17T00:00:00Z', to: '2026-10-17T01:00:00Z' },
    observed: { healthy_ms: 40_000, unavailable_ms: 10_000, unknown_ms: 10_000, excluded_ms: 0 },
    expected_ms: 60_000,
    monitoring_coverage: rate({ numerator_ms: 50_000, denominator_ms: 60_000 }),
    observed_availability: rate(),
    conservative_availability: rate({ denominator_ms: 60_000 }),
    interruptions: {
      count: 1,
      total_ms: 600_000,
      longest_ms: 600_000,
      open_count: 0,
      open_ms: 0,
      mean_time_to_recovery: rate({ numerator_ms: 600_000, denominator_ms: 1 }),
      by_category: { upstream_unavailable: 1 },
      component_count: 0,
      maintenance_count: 0,
    },
    recording_coverage: rate({ numerator_ms: 0, denominator_ms: 0, status: 'not_applicable' }),
    recording_gaps: { missing_ms: 0, gap_count: 0, longest_gap_ms: 0, damaged_count: 0, reasons: {} },
    media_accessibility: {
      rate: rate({ status: 'not_applicable', numerator_ms: 0, denominator_ms: 0 }),
      unverified_ms: 0,
      remote_unknown_ms: 0,
      verified_coverage_ms: 0,
    },
    metric_version: 1,
    precision: 'exact',
  }
}

function stub(overrides: {
  items?: Schema<'ReliabilityMetrics'>[]
  intervals?: unknown[]
  integrity?: Partial<Schema<'ReliabilityIntegrity'>>
  faults?: unknown[]
  precision?: string
}) {
  calls.request.mockImplementation(async (path: string) => {
    if (path.includes('/metrics')) return { items: overrides.items ?? [metrics('main'), metrics('sub')] }
    if (path.includes('/intervals')) return { items: overrides.intervals ?? [] }
    if (path.includes('/integrity'))
      return {
        owed_ms: 3_600_000,
        covered_ms: 3_000_000,
        missing_ms: 300_000,
        unverified_ms: 300_000,
        excluded_ms: 60_000,
        exclusions: { disabled: 60_000 },
        precision: overrides.precision ?? 'exact',
        ...overrides.integrity,
      }
    if (path.includes('/faults')) return { items: overrides.faults ?? [], categories: [] }
    return {}
  })
}

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <ChannelReliability channelId={channelID} />
    </QueryClientProvider>
  )
}

test('renders one panel per stream kind and never merges them', async () => {
  stub({})
  mount()
  await expect.element(page.getByTestId('reliability-metrics-main')).toBeVisible()
  await expect.element(page.getByTestId('reliability-metrics-sub')).toBeVisible()
  // Two kinds means two panels; there is no combined figure anywhere, because a
  // healthy sub stream must not stand in for a broken main one.
  await expect.element(page.getByTestId('reliability-metrics-recording')).not.toBeInTheDocument()
})

test('every rate is shown with the inputs behind it', async () => {
  stub({})
  mount()
  // The PRD's own arithmetic: 40s of 50s known is 80%.
  await expect.element(page.getByText('80.00%').first()).toBeVisible()
  // And the numerator/denominator travel beside it.
  await expect.element(page.getByText('分子 40 秒 · 分母 50 秒').first()).toBeVisible()
})

test('a rate with no value shows its reason instead of zero', async () => {
  stub({})
  mount()
  // recording_coverage is not_applicable in the fixture: rendering 0% would
  // report "nothing was recorded" as a total failure.
  await expect.element(page.getByText('不适用').first()).toBeVisible()
})

test('reports unjudgeable time beside the ratio rather than folding it in', async () => {
  const value = metrics('main')
  value.observed_availability = rate({ unjudgeable_ms: 10_000 })
  stub({ items: [value] })
  mount()
  await expect.element(page.getByText(/不可判定 10 秒/).first()).toBeVisible()
})

test('shows the recording split with exclusions kept apart', async () => {
  stub({})
  mount()
  await expect.element(page.getByTestId('reliability-completeness')).toBeVisible()
  await expect.element(page.getByText(/不要求录像：/)).toBeVisible()
  await expect.element(page.getByText(/停用\/关闭 1 分/)).toBeVisible()
})

test('states a degraded precision instead of implying exactness', async () => {
  stub({ precision: 'assumed' })
  mount()
  await expect.element(page.getByText(/策略时刻为回填值/)).toBeVisible()
})

test('draws a band per interval and explains that excluded time is not good news', async () => {
  stub({
    intervals: [
      {
        id: 'i1',
        kind: 'main',
        state: 'healthy',
        reason: 'frame_progress_observed',
        started_at: '2026-10-17T00:00:00Z',
        ended_at: '2026-10-17T00:30:00Z',
        first_observed_at: '2026-10-17T00:00:00Z',
        last_observed_at: '2026-10-17T00:30:00Z',
        observed_count: 3,
        duration_ms: 1_800_000,
      },
      {
        id: 'i2',
        kind: 'main',
        state: 'excluded',
        exclusion: 'disabled',
        reason: 'recording_disabled',
        started_at: '2026-10-17T00:30:00Z',
        ended_at: '2026-10-17T01:00:00Z',
        first_observed_at: '2026-10-17T00:30:00Z',
        last_observed_at: '2026-10-17T01:00:00Z',
        observed_count: 3,
        duration_ms: 1_800_000,
      },
    ],
  })
  mount()
  // Scoped to the timeline: "可用" also names a metrics fact, and asserting
  // unscoped would pass on the wrong element.
  const timeline = page.getByTestId('reliability-timeline')
  await expect.element(timeline).toBeVisible()
  await expect.element(timeline.getByText('可用', { exact: true })).toBeVisible()
  // Excluded time is labelled for what it is, not painted as availability.
  await expect.element(timeline.getByText('不要求（停用/未配置/维护）')).toBeVisible()
  // The band itself carries the reason, so a grey stretch is explicable rather
  // than merely grey. Asserted through the DOM attribute because the band is a
  // bare div with no accessible name.
  const bands = timeline.element().querySelectorAll('div[title]')
  const titles = Array.from(bands).map((node) => node.getAttribute('title'))
  expect(titles).toContain('excluded · 已停用或明确关闭')
})

test('lists faults and points at the operations page rather than duplicating it', async () => {
  stub({
    faults: [
      {
        id: 'f1',
        channel_id: channelID,
        channel_no: 1,
        kind: 'main',
        state: 'resolved',
        started_at: '2026-10-17T00:10:00Z',
        ended_at: '2026-10-17T00:20:00Z',
        duration_ms: 600_000,
        initial_reason: 'source_unavailable',
        category: 'upstream_unavailable',
        effective_category: 'upstream_unavailable',
        note: '',
        maintenance: false,
      },
    ],
  })
  mount()
  await expect.element(page.getByTestId('reliability-faults')).toBeVisible()
  await expect.element(page.getByText('10 分').first()).toBeVisible()
  await expect.element(page.getByText('在运维页查看与确认故障')).toBeVisible()
})

test('offers the four windows the requirement names plus a custom range', async () => {
  stub({})
  mount()
  for (const label of ['最近 24 小时', '最近 7 天', '最近 30 天', '最近 90 天']) {
    await expect.element(page.getByText(label)).toBeVisible()
  }
  await expect.element(page.getByLabelText('开始时间')).toBeVisible()
  await expect.element(page.getByLabelText('结束时间')).toBeVisible()
})
