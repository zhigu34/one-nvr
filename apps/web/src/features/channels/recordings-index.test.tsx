import { expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { RecordingsIndex } from './recordings-index'

vi.mock('@/features/foundation/hooks', () => ({
  useAPI: (path: string) => ({
    isPending: false,
    error: null,
    refetch: vi.fn(),
    data:
      path === '/api/v1/site'
        ? { timezone: 'Asia/Shanghai' }
        : {
            items: [
              {
                id: 'segment',
                start: '2026-10-05T00:00:00Z',
                end: '2026-10-05T00:01:00Z',
                bytes: 1024,
                state: 'ready',
                check_state: 'pool_unavailable',
                source_revision_id: 'revision',
                pool_id: 'pool',
              },
            ],
          },
  }),
}))
test('ready metadata does not claim a file is usable while its pool is unavailable', async () => {
  const view = await render(<RecordingsIndex channelId='channel' />)
  await expect.element(view.getByText('存储池不可用')).toBeVisible()
  await expect
    .element(view.getByText('可用', { exact: true }))
    .not.toBeInTheDocument()
})
