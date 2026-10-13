import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { useAuthStore } from '@/stores/auth-store'
import { Detection } from './index'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

const channelID = '00000000-0000-4000-8000-000000000001'

function fixture(enabled: boolean) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  calls.request.mockImplementation(async (path: string) => {
    if (path.startsWith('/api/v1/channels?'))
      return {
        items: [
          {
            id: channelID,
            channel_no: 5,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 1,
            permissions: ['configure'],
          },
        ],
        next_cursor: null,
      }
    if (path === '/api/v1/detection/configs')
      return {
        items: [
          { channel_id: channelID, enabled, stream: 'sub', fps: 5, labels: ['person'], version: 1 },
        ],
      }
    if (path === '/api/v1/detection/runtime') return { revision: null }
    if (path.startsWith('/api/v1/detection/zones?')) return { items: [] }
    if (path.startsWith('/api/v1/detection/rules?')) return { items: [] }
    throw new Error(`unexpected path ${path}`)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return client
}

const view = (client: QueryClient) => (
  <QueryClientProvider client={client}>
    <Detection />
  </QueryClientProvider>
)

test('zones and rules stay behind a disclosure until asked for', async () => {
  const screen = await render(view(fixture(true)))
  await expect.element(screen.getByTestId('detection-row')).toBeVisible()
  // Zone drawing is not part of setting up 32 channels, so the row stays one
  // line: the panels mount only once the operator opens them.
  const toggle = screen.getByRole('button', { name: 'CH05 大门 区域与规则' })
  await expect.element(toggle).toBeVisible()
  expect(document.querySelector('[data-testid="zone-canvas"]')).toBeNull()

  await toggle.click()
  await expect.element(screen.getByTestId('zone-canvas')).toBeVisible()
  await expect.element(screen.getByRole('button', { name: '新建规则' })).toBeVisible()
})

test('a channel without detection says why zones cannot be calibrated', async () => {
  const screen = await render(view(fixture(false)))
  await screen.getByRole('button', { name: 'CH05 大门 区域与规则' }).click()
  // The warning is the honest state: without detection there is no frame to
  // draw against and no events for a rule to match.
  await expect.element(screen.getByRole('status')).toHaveTextContent(/未启用检测/)
})
