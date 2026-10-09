import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import type { Schema } from '@/lib/types'
import { SourceImportControls } from './source-import-controls'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => vi.clearAllMocks())
const channel: Schema<'Channel'> = {
  id: '00000000-0000-4000-8000-000000000001',
  channel_no: 1,
  channel_name: 'Gate',
  channel_group: '',
  enabled: true,
  version: 4,
  permissions: ['configure'],
}
test('file preview sends multipart and does not submit or test until explicitly selected', async () => {
  const row: Schema<'ImportItem'> = {
    row: 1,
    channel_id: channel.id,
    expected_version: 4,
    state: 'draft',
    differences: ['new_source'],
    warnings: [],
    errors: [],
    job_id: null,
  }
  calls.request.mockImplementation(async (path: string) =>
    path.endsWith('/preview')
      ? {
          batch_id: 'batch',
          expires_at: new Date(Date.now() + 10000).toISOString(),
          items: [row],
        }
      : { batch_id: 'batch', state: 'preview', items: [row] }
  )
  const view = await render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <SourceImportControls channels={[channel]} />
    </QueryClientProvider>
  )
  const input = view
    .getByLabelText('导入配置文件', { exact: true })
    .element() as HTMLInputElement
  const transfer = new DataTransfer()
  transfer.items.add(
    new File(
      [
        'channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,门,192.168.33.20,554,fixture,fixture-pass,/main,/sub',
      ],
      'channels.csv',
      { type: 'text/csv' }
    )
  )
  input.files = transfer.files
  input.dispatchEvent(new Event('change', { bubbles: true }))
  await userEvent.click(
    view.getByRole('button', { name: '生成导入预览', exact: true })
  )
  await expect.element(view.getByText('第 1 行 · 待确认')).toBeVisible()
  expect(calls.request.mock.calls[0][1].body).toBeInstanceOf(FormData)
  expect(
    calls.request.mock.calls.every(
      ([path]) =>
        !String(path).endsWith('/test') && path !== '/api/v1/source-imports'
    )
  ).toBe(true)
  expect(input.files?.length).toBe(0)
})

test('testing updates the channel version and requires explicit confirmation before submitting', async () => {
  const row: Schema<'ImportItem'> = {
    row: 1,
    channel_id: channel.id,
    expected_version: 4,
    state: 'draft',
    password_action: 'replace',
    differences: ['new_source'],
    warnings: [],
    errors: [],
    job_id: null,
  }
  let tested = false
  calls.request.mockImplementation(async (path: string) => {
    if (path.endsWith('/test')) {
      tested = true
      return { job_id: 'job', state: 'queued' }
    }
    if (path.endsWith('/preview'))
      return {
        batch_id: 'batch',
        expires_at: new Date(Date.now() + 300000).toISOString(),
        items: [row],
      }
    return {
      batch_id: 'batch',
      state: 'preview',
      items: [tested ? { ...row, state: 'tested', expected_version: 5 } : row],
    }
  })
  const view = await render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <SourceImportControls channels={[channel]} />
    </QueryClientProvider>
  )
  const input = view
      .getByLabelText('导入配置文件', { exact: true })
      .element() as HTMLInputElement,
    transfer = new DataTransfer()
  transfer.items.add(new File(['fixture'], 'channels.csv'))
  input.files = transfer.files
  await userEvent.click(
    view.getByRole('button', { name: '生成导入预览', exact: true })
  )
  await expect.element(view.getByText('第 1 行 · 待确认')).toBeVisible()
  await userEvent.click(
    view.getByRole('button', { name: '测试选中行', exact: true })
  )
  await expect.element(view.getByText('第 1 行 · 测试通过')).toBeVisible()
  await expect
    .element(
      view.getByRole('button', { name: '重新确认第 1 行最新版本', exact: true })
    )
    .toBeVisible()
  await userEvent.click(
    view.getByRole('button', { name: '重新确认第 1 行最新版本', exact: true })
  )
  await userEvent.click(
    view.getByLabelText(
      '已确认选中行的目标、密码处理和当前版本；应用会切换对应摄像头'
    )
  )
  await userEvent.click(
    view.getByRole('button', { name: '确认并应用选中行', exact: true })
  )
  const request = calls.request.mock.calls.find(
    ([path]) => path === '/api/v1/source-imports'
  )
  expect(JSON.parse(request![1].body).items[0].expected_version).toBe(5)
})

test('opening a batch link restores safe progress without testing or submitting', async () => {
  const row: Schema<'ImportItem'> = {
    row: 1,
    channel_id: channel.id,
    expected_version: 5,
    state: 'failed',
    password_action: 'replace',
    differences: ['source_configuration'],
    warnings: [],
    errors: ['channel_version_changed'],
    job_id: null,
  }
  calls.request.mockResolvedValue({
    batch_id: '00000000-0000-4000-8000-000000000009',
    state: 'partial',
    expires_at: new Date(Date.now() + 300000).toISOString(),
    items: [row],
  })
  const view = await render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <SourceImportControls
        channels={[channel]}
        initialBatch='00000000-0000-4000-8000-000000000009'
      />
    </QueryClientProvider>
  )
  await expect
    .element(view.getByText('通道配置已改变，请重新确认'))
    .toBeVisible()
  expect(
    calls.request.mock.calls.every(
      ([, init]) => !init?.method || init.method === 'GET'
    )
  ).toBe(true)
})

test.each([false, true])(
  'cleared target import omits first mode even after remapping: %s',
  async (remap) => {
    const target = {
      ...channel,
      id: '00000000-0000-4000-8000-000000000002',
      channel_no: 2,
    }
    const row: Schema<'ImportItem'> = {
      row: 1,
      channel_id: channel.id,
      expected_version: 4,
      state: 'tested',
      password_action: 'clear',
      differences: ['new_source'],
      warnings: [],
      errors: [],
      job_id: null,
      requires_initial_recording_mode: remap,
    }
    calls.request.mockImplementation(async (path: string) =>
      path.endsWith('/source/status')
        ? { requires_initial_recording_mode: false }
        : {
            batch_id: '00000000-0000-4000-8000-000000000009',
            state: 'preview',
            expires_at: new Date(Date.now() + 300000).toISOString(),
            items: [row],
          }
    )
    const view = await render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <SourceImportControls
          channels={[channel, target]}
          initialBatch='00000000-0000-4000-8000-000000000009'
        />
      </QueryClientProvider>
    )
    await expect.element(view.getByText('第 1 行 · 测试通过')).toBeVisible()
    if (remap) {
      await userEvent.click(view.getByLabelText('第 1 行目标通道'))
      await userEvent.click(view.getByRole('option', { name: /^CH02 · / }))
    }
    await userEvent.click(view.getByLabelText('选择第 1 行'))
    await userEvent.click(
      view.getByLabelText(
        '已确认选中行的目标、密码处理和当前版本；应用会切换对应摄像头'
      )
    )
    await userEvent.click(
      view.getByRole('button', { name: '确认并应用选中行', exact: true })
    )
    const call = calls.request.mock.calls.find(
      ([path]) => path === '/api/v1/source-imports'
    )
    expect(call).toBeDefined()
    expect(JSON.parse(call![1].body).items[0]).not.toHaveProperty(
      'first_recording_mode'
    )
  }
)
