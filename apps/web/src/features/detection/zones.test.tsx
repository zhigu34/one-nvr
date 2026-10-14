import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema } from '@/lib/types'
import { ZoneWorkbench } from './zones'

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

function fixture(zones: Schema<'DetectionZone'>[] = []) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  const mutations: { path: string; init: RequestInit }[] = []
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method && init.method !== 'GET' ? init.method : 'GET'
    if (method !== 'GET') mutations.push({ path, init: init as RequestInit })
    if (method === 'DELETE') return {}
    if (path.startsWith('/api/v1/detection/zones?')) return { items: zones }
    if (path === '/api/v1/detection/zones') return { id: 'zone-new', name: '门口' }
    if (path.startsWith('/api/v1/detection/rules?')) return { items: [] }
    if (path.startsWith('/api/v1/channels?')) return { items: [], next_cursor: null }
    throw new Error(`unexpected path ${path}`)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return { mutations, client }
}

function paint(
  client: QueryClient,
  props: { channelID?: string; detectionEnabled?: boolean } = {}
) {
  return (
    <QueryClientProvider client={client}>
      <ZoneWorkbench
        channelID={props.channelID ?? channelID}
        detectionEnabled={props.detectionEnabled ?? true}
      />
    </QueryClientProvider>
  )
}

/** Draws one vertex at a fraction of the canvas, however wide it renders. */
function press(canvas: HTMLElement, fx: number, fy: number) {
  const box = canvas.getBoundingClientRect()
  canvas.dispatchEvent(
    new PointerEvent('pointerdown', {
      bubbles: true,
      button: 0,
      clientX: box.left + box.width * fx,
      clientY: box.top + box.height * fy,
    })
  )
}

test('drawing a polygon sends normalized coordinates', async () => {
  const { client, mutations } = fixture()
  const view = await render(paint(client))
  const canvas = view.getByTestId('zone-canvas')
  await expect.element(canvas).toBeVisible()

  // The canvas stands for the detector's own frame; clicking a quarter across
  // must come out as 0.25 regardless of the element's rendered size, which is
  // the point of storing normalized coordinates.
  press(canvas.element() as HTMLElement, 0.25, 0.33)
  press(canvas.element() as HTMLElement, 0.75, 0.33)
  press(canvas.element() as HTMLElement, 0.5, 0.8)
  // The form saves a name and a polygon together, so the button enables only
  // once both are present.
  const save = view.getByRole('button', { name: /保存区域（3 点）/ })
  await expect.element(save).toBeDisabled()

  await userEvent.fill(view.getByLabelText('区域名称'), '门口')
  await expect.element(save).toBeEnabled()
  await save.click()

  await vi.waitFor(() => expect(mutations.length).toBe(1))
  const body = JSON.parse(String(mutations[0].init.body))
  expect({ method: mutations[0].init.method, path: mutations[0].path }).toEqual({
    method: 'POST',
    path: '/api/v1/detection/zones',
  })
  expect(body.channel_id).toBe(channelID)
  expect(body.name).toBe('门口')
  expect(body.coordinates).toEqual([
    { x: 0.25, y: 0.33 },
    { x: 0.75, y: 0.33 },
    { x: 0.5, y: 0.8 },
  ])
  // The reference geometry is the server's business: it reads the channel's
  // own source test, so the browser must not get to claim it.
  expect(body.reference_width).toBeUndefined()
  expect(body.reference_height).toBeUndefined()
})

test('a drawing below three vertices cannot be saved', async () => {
  const { client, mutations } = fixture()
  const view = await render(paint(client))
  const canvas = view.getByTestId('zone-canvas')
  await expect.element(canvas).toBeVisible()
  press(canvas.element() as HTMLElement, 0.2, 0.5)
  press(canvas.element() as HTMLElement, 0.6, 0.5)
  await expect.element(view.getByRole('button', { name: /保存区域（2 点）/ })).toBeDisabled()
  expect(mutations).toHaveLength(0)
})

test('saved zones render, and a stale drawing is called out', async () => {
  const { client } = fixture([
    {
      id: 'zone-1',
      channel_id: channelID,
      name: '门口',
      coordinates: [
        { x: 0.1, y: 0.1 },
        { x: 0.4, y: 0.1 },
        { x: 0.4, y: 0.4 },
      ],
      objects: ['person'],
      needs_calibration: false,
    },
    {
      id: 'zone-2',
      channel_id: channelID,
      name: '院子',
      coordinates: [
        { x: 0.5, y: 0.5 },
        { x: 0.9, y: 0.5 },
        { x: 0.9, y: 0.9 },
      ],
      objects: [],
      needs_calibration: true,
    },
  ])
  const view = await render(paint(client))
  await expect.element(view.getByText('门口')).toBeVisible()
  // A drawing made against a different aspect ratio is flagged instead of
  // being silently reinterpreted: only the operator knows where the zone now
  // belongs.
  await expect.element(view.getByRole('alert').first()).toHaveTextContent(/旧画面比例/)
  expect(document.querySelectorAll('[data-testid="zone-shape"]')).toHaveLength(2)
})

// The workbench serves the frame the detector analyzes without a click when
// the channel runs detection; without detection the canvas collapses instead
// of showing a black void and refuses pointer input. The frame is loaded by
// the browser straight from the API (an <img> src), so presence of the image
// element is what the auto-load asserts on.
test('the frame auto-loads on an enabled channel and stays collapsed otherwise', async () => {
  const { client } = fixture()
  const view = await render(paint(client, { detectionEnabled: true }))
  await expect.element(view.getByTestId('zone-canvas')).toBeVisible()
  await expect
    .element(view.getByRole('img', { name: '检测画面' }))
    .toHaveAttribute(
      'src',
      `/api/v1/detection/frame?channel_id=${channelID}&frame=1`
    )
  await view.unmount()

  const disabled = await render(paint(client, { detectionEnabled: false }))
  const canvas = disabled.getByTestId('zone-canvas')
  await expect.element(canvas).toBeVisible()
  expect(disabled.getByRole('img', { name: '检测画面' }).query()).toBeNull()
  // The inert canvas takes no input: clicking must not accumulate vertices,
  // and the save button stays at 0 points.
  press(canvas.element() as HTMLElement, 0.3, 0.4)
  await expect
    .element(
      disabled.getByRole('button', { name: '保存区域（0 点）' })
    )
    .toBeVisible()
})
