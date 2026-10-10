import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { LivePreview } from './index'
import type { PlayerState } from './player'
import type { LiveMediaInfo } from './media-info'

const calls = vi.hoisted(() => ({
  request: vi.fn(),
  started: vi.fn(),
  stopped: vi.fn(),
  update: null as ((state: PlayerState) => void) | null,
  media: null as ((info: LiveMediaInfo) => void) | null,
}))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
vi.mock('./player', () => ({
  LivePlayer: class {
    constructor(
      _video: unknown,
      channel: string,
      _stream: unknown,
      update: (state: PlayerState) => void,
      media: (info: LiveMediaInfo) => void
    ) {
      calls.started(channel)
      calls.update = update
      calls.media = media
    }
    start() {
      return Promise.resolve()
    }
    stop() {
      calls.stopped()
    }
  },
}))
afterEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
})
async function fixture() {
  calls.request.mockResolvedValue({
    items: Array.from({ length: 32 }, (_, index) => ({
      id: `channel-${index + 1}`,
      channel_no: index + 1,
      channel_name: `通道 ${index + 1}`,
      version: 1,
      permissions: index === 31 ? ['playback'] : ['live'],
    })),
    next_cursor: null,
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <LivePreview />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return render(<RouterProvider router={router} />)
}
test('live permission, channel grouping and active layout determine media connections', async () => {
  const screen = await fixture()
  await expect
    .element(screen.getByRole('button', { name: 'CH01 通道 1', exact: true }))
    .toBeVisible()
  await screen.getByRole('button', { name: 'CH01 通道 1', exact: true }).click()
  expect(calls.started).toHaveBeenCalledWith('channel-1')
  await screen.getByRole('button', { name: '16 画面', exact: true }).click()
  expect(document.querySelectorAll('[data-testid=live-video]')).toHaveLength(16)
  await screen.getByRole('button', { name: '下一组', exact: true }).click()
  await expect
    .element(screen.getByRole('button', { name: 'CH17 通道 17', exact: true }))
    .toBeVisible()
  expect(document.body.textContent).not.toContain('CH32 通道 32')
  await screen.getByRole('button', { name: '停止全部', exact: true }).click()
  expect(calls.stopped).toHaveBeenCalled()
})
test('stored forbidden channels never create a media connection', async () => {
  localStorage.setItem(
    'one-nvr:live-view:',
    JSON.stringify({ layout: 4, channels: ['channel-32', 'channel-1'] })
  )
  const screen = await fixture()
  await expect
    .element(screen.getByRole('button', { name: 'CH01 通道 1', exact: true }))
    .toBeVisible()
  expect(calls.started).toHaveBeenCalledWith('channel-1')
  expect(calls.started).not.toHaveBeenCalledWith('channel-32')
  await screen.unmount()
  expect(calls.stopped).toHaveBeenCalled()
})
test('a playing tile shows the decoded media facts instead of a bare status', async () => {
  const screen = await fixture()
  await screen.getByRole('button', { name: 'CH01 通道 1', exact: true }).click()
  await vi.waitFor(() => expect(calls.update).toBeTypeOf('function'))
  await act(async () => {
    calls.update!({ phase: 'playing', message: '播放中', stream: 'main' })
    calls.media!({
      width: 2560,
      height: 1440,
      fps: 25,
      videoCodec: 'H264',
      audioCodec: 'PCMA',
    })
  })
  await expect.element(screen.getByText('主流', { exact: true })).toBeVisible()
  await expect
    .element(screen.getByTestId('live-media-info'))
    .toHaveTextContent('2560×1440 · 25fps · 直通 · 音 PCMA')
  // The tile no longer repeats the obvious playing state.
  expect(document.body.textContent).not.toContain('播放中')
})
