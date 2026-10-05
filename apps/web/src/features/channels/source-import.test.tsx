import { expect, test } from 'vitest'
import { render } from 'vitest-browser-react'
import type { Schema } from '@/lib/types'
import { ImportRows } from './source-import'

test('preview, failure, conflict and skip retain per-row descriptions without an automatic action', async () => {
  const row: Schema<'ImportItem'> = {
    row: 1,
    channel_id: null,
    expected_version: null,
    state: 'draft',
    differences: ['source_configuration'],
    warnings: [],
    errors: [],
    job_id: null,
  }
  const view = await render(
    <ImportRows
      items={[
        row,
        {
          ...row,
          row: 2,
          state: 'failed',
          errors: ['channel_version_changed'],
        },
        { ...row, row: 3, state: 'failed', errors: ['source_test_failed'] },
        {
          ...row,
          row: 4,
          state: 'skipped',
          warnings: ['empty_source_skipped'],
        },
      ]}
    />
  )
  await expect.element(view.getByText('待确认')).toBeVisible()
  await expect
    .element(view.getByText('通道配置已改变，请重新确认'))
    .toBeVisible()
  await expect.element(view.getByText('取流测试失败')).toBeVisible()
  await expect.element(view.getByText('空源行已跳过，保留原配置')).toBeVisible()
  await expect
    .element(view.getByRole('button', { name: '应用配置' }))
    .not.toBeInTheDocument()
})

// A preview is safe metadata only. It must never submit, test or activate media by itself.
