import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page } from 'vitest/browser'
import type { Schema } from '@/lib/types'
import { AbnormalChannels } from './abnormal-channels'

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

function row(over: Partial<Schema<'ReliabilityChannelSummary'>> = {}): Schema<'ReliabilityChannelSummary'> {
  return {
    channel_id: '00000000-0000-4000-8000-000000000001',
    channel_no: 1,
    channel_name: '前门',
    kind: 'main',
    healthy_ms: 86_000_000,
    unavailable_ms: 0,
    unknown_ms: 0,
    excluded_ms: 0,
    expected_ms: 86_400_000,
    owed_ms: 0,
    covered_ms: 0,
    fault_count: 0,
    // A day of observation: enough to be comparable.
    observed_ms: 86_000_000,
    sample_sufficient: true,
    ...over,
  }
}

function mount(rows: Schema<'ReliabilityChannelSummary'>[]) {
  calls.request.mockResolvedValue({ items: rows })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <AbnormalChannels />
    </QueryClientProvider>
  )
}

test('names the channels that had interruptions, worst first', async () => {
  mount([
    row({ channel_id: 'a', channel_no: 1, channel_name: '前门', unavailable_ms: 60_000, fault_count: 1 }),
    row({ channel_id: 'b', channel_no: 2, channel_name: '后门', unavailable_ms: 600_000, fault_count: 2 }),
    row({ channel_id: 'c', channel_no: 3, channel_name: '天台' }),
  ])
  await expect.element(page.getByTestId('abnormal-channels')).toBeVisible()
  await expect.element(page.getByText('后门')).toBeVisible()
  await expect.element(page.getByText('前门')).toBeVisible()
  // The healthy channel is not listed: this card is a problem list, not a roster.
  await expect.element(page.getByText('天台')).not.toBeInTheDocument()
})

test('a clean day says so rather than showing an empty box', async () => {
  mount([row()])
  await expect.element(page.getByText('有观测的通道没有出现中断。')).toBeVisible()
})

test('channels with too little observation are excluded and counted apart', async () => {
  // PRD REL-05: a barely observed channel must not be presented as the worst
  // one. It is reported as unjudged instead.
  mount([
    row({
      channel_id: 'thin',
      channel_no: 9,
      channel_name: '少观测',
      unavailable_ms: 300_000,
      fault_count: 1,
      observed_ms: 600_000,
      sample_sufficient: false,
    }),
  ])
  await expect.element(page.getByTestId('abnormal-channels')).toBeVisible()
  await expect.element(page.getByText('少观测')).not.toBeInTheDocument()
  await expect.element(page.getByText(/观察时长不足/)).toBeVisible()
})

test('hides the card entirely when the evidence cannot be read', async () => {
  calls.request.mockRejectedValue(new Error('boom'))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <AbnormalChannels />
    </QueryClientProvider>
  )
  // Claiming "no abnormal channels" when the read failed is the one thing this
  // card must not do, so it renders nothing at all.
  await expect.element(page.getByTestId('abnormal-channels')).not.toBeInTheDocument()
})
