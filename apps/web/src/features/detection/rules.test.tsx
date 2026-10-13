import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import type { PageData, Schema } from '@/lib/types'
import { RulePanel } from './rules'

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
const gate = {
  id: '00000000-0000-4000-8000-0000000000z1',
  channel_id: channelID,
  name: '门口',
  coordinates: [
    { x: 0.1, y: 0.1 },
    { x: 0.4, y: 0.1 },
    { x: 0.4, y: 0.4 },
  ],
  objects: [],
  needs_calibration: false,
}

function fixture(rules: Schema<'DetectionRule'>[] = [], zones: Schema<'DetectionZone'>[] = [gate]) {
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
    if (path.startsWith('/api/v1/detection/rules?')) return { items: rules }
    if (path.startsWith('/api/v1/detection/zones?')) return { items: zones }
    if (path.startsWith('/api/v1/channels?'))
      return {
        items: [
          {
            id: channelID,
            channel_no: 1,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 1,
            permissions: ['configure'],
          },
        ],
        next_cursor: null,
      } satisfies PageData<Schema<'Channel'>>
    if (path === '/api/v1/detection/rules') return { id: 'rule-new', name: '门口进入' }
    throw new Error(`unexpected path ${path}`)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return { mutations, client }
}

const view = (client: QueryClient) => (
  <QueryClientProvider client={client}>
    <RulePanel channelID={channelID} onChanged={() => {}} />
  </QueryClientProvider>
)

test('a zone rule cannot be saved without a zone', async () => {
  const { client, mutations } = fixture()
  const screen = await render(view(client))
  await screen.getByRole('button', { name: '新建规则' }).click()
  await expect.element(screen.getByTestId('rule-editor')).toBeVisible()
  // The kind decides the shape: an appearance rule needs no zone, so it can be
  // saved as soon as it has a name.
  await userEvent.fill(screen.getByLabelText('规则名称'), '有人经过')
  const save = screen.getByRole('button', { name: '保存规则' })
  await expect.element(save).toBeEnabled()

  // Switching to a zone rule demands the zone before saving: the server would
  // refuse a rule that can never fire, and the form must not invite writing one.
  await screen.getByLabelText('规则类型').click()
  await screen.getByRole('option', { name: '区域进入', exact: true }).click()
  await expect.element(save).toBeDisabled()
  await screen.getByLabelText('区域').click()
  await screen.getByRole('option', { name: '门口', exact: true }).click()
  await expect.element(save).toBeEnabled()
  await save.click()

  await vi.waitFor(() => expect(mutations.length).toBe(1))
  const body = JSON.parse(String(mutations[0].init.body))
  expect({ method: mutations[0].init.method, path: mutations[0].path }).toEqual({
    method: 'POST',
    path: '/api/v1/detection/rules',
  })
  expect(body).toMatchObject({
    channel_id: channelID,
    name: '有人经过',
    kind: 'enter',
    zone_id: gate.id,
    categories: ['person'],
    severity: 'medium',
  })
})

test('a dwell rule carries its threshold, an appearance rule carries no zone', async () => {
  const { client, mutations } = fixture()
  const screen = await render(view(client))
  await screen.getByRole('button', { name: '新建规则' }).click()
  await userEvent.fill(screen.getByLabelText('规则名称'), '门口逗留')
  await screen.getByLabelText('规则类型').click()
  await screen.getByRole('option', { name: '区域停留', exact: true }).click()
  await screen.getByLabelText('区域').click()
  await screen.getByRole('option', { name: '门口', exact: true }).click()
  await userEvent.fill(screen.getByLabelText('停留秒数'), '30')
  await userEvent.fill(screen.getByLabelText('布防开始时间'), '22:00')
  await userEvent.fill(screen.getByLabelText('布防结束时间'), '06:00')
  await screen.getByRole('button', { name: '保存规则' }).click()

  await vi.waitFor(() => expect(mutations.length).toBe(1))
  const body = JSON.parse(String(mutations[0].init.body))
  expect(body).toMatchObject({
    kind: 'dwell',
    zone_id: gate.id,
    dwell_seconds: 30,
    arm_from: '22:00',
    arm_to: '06:00',
  })
})

test('rules render with their conditions and remove by id', async () => {
  const { client, mutations } = fixture([
    {
      id: 'rule-1',
      channel_id: channelID,
      name: '门口进入',
      kind: 'enter',
      zone_id: gate.id,
      categories: ['person'],
      min_score: 0.5,
      arm_from: '22:00',
      arm_to: '06:00',
      dwell_seconds: null,
      severity: 'high',
      cooldown_seconds: 60,
      enabled: true,
    },
  ])
  const screen = await render(view(client))
  await expect.element(screen.getByText('门口进入')).toBeVisible()
  // The row states the conditions, not just the name: an operator must be able
  // to see why a rule did or did not fire without opening the editor.
  await expect.element(screen.getByTestId('rule-row')).toHaveTextContent(/区域进入/)
  await expect.element(screen.getByTestId('rule-row')).toHaveTextContent(/门口/)
  await expect.element(screen.getByTestId('rule-row')).toHaveTextContent(/22:00–06:00/)
  await expect.element(screen.getByTestId('rule-row')).toHaveTextContent(/置信 ≥ 50%/)

  await screen.getByRole('button', { name: '删除规则 门口进入' }).click()
  await vi.waitFor(() => expect(mutations.length).toBe(1))
  expect(mutations[0].path).toBe(`/api/v1/detection/rules/rule-1?channel_id=${channelID}`)
  expect(mutations[0].init.method).toBe('DELETE')
})

test('a dwell threshold on a zone that already enters is refused before saving', async () => {
  const { client, mutations } = fixture(
    [
      {
        id: 'rule-1',
        channel_id: channelID,
        name: '门口进入',
        kind: 'enter',
        zone_id: gate.id,
        categories: ['person'],
        min_score: 0,
        arm_from: '',
        arm_to: '',
        dwell_seconds: null,
        severity: 'high',
        cooldown_seconds: 60,
        enabled: true,
      },
    ],
    [{ ...gate, id: gate.id }]
  )
  const screen = await render(view(client))
  await screen.getByRole('button', { name: '新建规则' }).click()
  await userEvent.fill(screen.getByLabelText('规则名称'), '门口逗留')
  await screen.getByLabelText('规则类型').click()
  await screen.getByRole('option', { name: '区域停留', exact: true }).click()
  await screen.getByLabelText('区域').click()
  await screen.getByRole('option', { name: '门口', exact: true }).click()
  // The detector keeps one entry decision per zone and delays it by the dwell
  // threshold, so this combination cannot mean "enter now, dwell later".
  await expect.element(screen.getByRole('alert')).toHaveTextContent(/已有进入规则/)
  await expect.element(screen.getByRole('button', { name: '保存规则' })).toBeDisabled()
  expect(mutations).toHaveLength(0)
})
