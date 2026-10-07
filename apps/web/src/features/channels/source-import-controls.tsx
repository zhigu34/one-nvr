import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { apiRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI } from '@/features/foundation/hooks'
import { Notices } from '@/features/foundation/ui'
import { sourceCommand, downloadLocal } from './api'
import { ImportRows } from './source-import'
import { useNow } from './use-now'

function selections(
  items: Schema<'ImportItem'>[]
): Record<number, Schema<'ImportSelection'>> {
  return Object.fromEntries(
    items
      .filter((item) => item.channel_id && item.expected_version)
      .map((item) => [
        item.row,
        {
          row: item.row,
          channel_id: item.channel_id!,
          expected_version: item.expected_version!,
          identity_intent: item.differences.includes('new_source')
            ? 'replace'
            : 'modify',
          password_action: item.password_action || 'keep',
          import_name: true,
          ...(item.requires_initial_recording_mode
            ? { first_recording_mode: 'continuous' as const }
            : {}),
        },
      ])
  )
}
type Selection = Schema<'ImportSelection'>
export function SourceImportControls({
  channels,
  initialBatch = '',
}: {
  initialBatch?: string
  channels: Schema<'Channel'>[]
}) {
  const now = useNow()
  const client = useQueryClient(),
    controller = useRef<AbortController | null>(null)
  const [preview, setPreview] = useState<Schema<'ImportPreview'> | null>(null),
    [selection, setSelection] = useState<Record<number, Selection>>({}),
    [selectedRows, setRows] = useState<number[]>([])
  const [pending, setPending] = useState(false),
    [notice, setNotice] = useState(''),
    [error, setError] = useState(''),
    [confirmation, setConfirmation] = useState(false)
  const progress = useAPI<Schema<'ImportProgress'>>(
    `/api/v1/source-imports/${preview?.batch_id || 'none'}`,
    !!preview,
    2000
  )
  useEffect(() => () => controller.current?.abort(), [])
  useEffect(() => {
    if (!initialBatch) return
    const abort = new AbortController()
    controller.current = abort
    apiRequest<Schema<'ImportProgress'>>(
      `/api/v1/source-imports/${encodeURIComponent(initialBatch)}`,
      { signal: abort.signal }
    )
      .then((value) => {
        if (abort.signal.aborted) return
        setPreview({
          batch_id: value.batch_id,
          expires_at: value.expires_at || new Date(0).toISOString(),
          items: value.items,
        })
        setSelection(selections(value.items))
        setRows([])
        setNotice('批任务已恢复，请查看逐行结果')
      })
      .catch((e) => {
        if (!abort.signal.aborted)
          setError(e instanceof Error ? e.message : '无法恢复批任务')
      })
    return () => abort.abort()
  }, [initialBatch])
  const items = progress.data?.items || preview?.items || [],
    busy = ['submitted', 'running'].includes(progress.data?.state || '')
  function update(row: number, change: Partial<Selection>) {
    setSelection((previous) => ({
      ...previous,
      [row]: { ...previous[row], ...change },
    }))
    setConfirmation(false)
  }
  async function request(kind: 'test' | 'submit' | 'retry' | 'cancel') {
    if (!preview || pending) return
    const abort = new AbortController()
    controller.current = abort
    setPending(true)
    setError('')
    let chosenSelections: Selection[] = []
    let body: unknown = {},
      path = `/api/v1/source-imports/${preview.batch_id}/${kind}`
    if (kind === 'test') body = { rows: selectedRows }
    if (kind === 'submit' || kind === 'retry') {
      const eligible = items.filter(
        (item) =>
          selectedRows.includes(item.row) &&
          (kind === 'retry'
            ? item.state === 'failed'
            : ['draft', 'tested'].includes(item.state))
      )
      const chosen = eligible.map((item) => selection[item.row])
      chosenSelections = chosen
      if (
        chosen.some((value) => !value?.channel_id || !value.expected_version) ||
        chosen.length === 0
      ) {
        setError('请选择可执行的行和目标通道')
        setPending(false)
        return
      }
      body =
        kind === 'submit'
          ? { batch_id: preview.batch_id, items: chosen }
          : { items: chosen }
      if (kind === 'submit') path = '/api/v1/source-imports'
    }
    try {
      if (kind === 'submit' || kind === 'retry') {
        const chosen: Selection[] = []
        // Re-read the mapped target: a cleared channel is not necessarily new,
        // and a user may have changed the original preview mapping.
        for (const value of chosenSelections) {
          const status = await apiRequest<Schema<'ChannelSourceStatus'>>(
            `/api/v1/channels/${value.channel_id}/source/status`,
            { signal: abort.signal }
          )
          const { first_recording_mode: first, ...rest } = value
          chosen.push(
            status.requires_initial_recording_mode
              ? { ...rest, first_recording_mode: first || 'continuous' }
              : rest
          )
        }
        body =
          kind === 'submit'
            ? { batch_id: preview.batch_id, items: chosen }
            : { items: chosen }
      }
      await sourceCommand(path, body, undefined, abort.signal)
      if (!abort.signal.aborted) {
        setNotice(
          kind === 'cancel'
            ? '取消已请求，已开始的切换会安全完成'
            : kind === 'test'
              ? '测试已排队，不会应用配置'
              : '批任务已排队，请查看逐行结果'
        )
        setConfirmation(false)
        await client.invalidateQueries()
      }
    } catch (e) {
      if (!abort.signal.aborted)
        setError(e instanceof Error ? e.message : '操作失败')
    } finally {
      if (!abort.signal.aborted) setPending(false)
    }
  }
  return (
    <section className='grid gap-4'>
      <Notices error={error} notice={notice} />
      <p className='text-sm text-muted-foreground'>
        预览仅生成加密草稿，不会改变取流和录像。确认后逐行测试并应用，失败不会重做成功行。默认保留目标通道已有录像策略和存储池。
      </p>
      <Button
        type='button'
        variant='outline'
        onClick={() =>
          downloadLocal(
            'one-nvr-channels.csv',
            new Blob(
              [
                '\uFEFFchannel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,大门,192.168.33.20,554,admin,,/main,/sub\n',
              ],
              { type: 'text/csv;charset=utf-8' }
            )
          )
        }
      >
        下载 CSV 模板
      </Button>
      <form
        className='grid gap-3'
        onSubmit={async (event) => {
          event.preventDefault()
          if (pending || busy) return
          const form = event.currentTarget,
            upload = form.elements.namedItem('file') as HTMLInputElement,
            file = upload.files?.[0]
          if (!file) {
            setError('请选择配置文件')
            return
          }
          const data = new FormData(form),
            abort = new AbortController()
          controller.current = abort
          setPending(true)
          setError('')
          setNotice('')
          try {
            const value = await apiRequest<Schema<'ImportPreview'>>(
              '/api/v1/source-imports/preview',
              { method: 'POST', body: data, signal: abort.signal }
            )
            if (!abort.signal.aborted) {
              setPreview(value)
              setRows(
                value.items
                  .filter(
                    (item) =>
                      item.state === 'draft' &&
                      item.errors.length === 0 &&
                      item.channel_id
                  )
                  .map((item) => item.row)
              )
              setSelection(selections(value.items))
              setConfirmation(false)
              setNotice('预览已生成，尚未测试或应用')
            }
          } catch (e) {
            if (!abort.signal.aborted)
              setError(e instanceof Error ? e.message : '预览失败')
          } finally {
            upload.value = ''
            if (!abort.signal.aborted) setPending(false)
          }
        }}
      >
        <label className='grid gap-2 text-sm'>
          文件格式
          <select name='format' className='rounded-md border bg-background p-2'>
            <option value='csv'>CSV</option>
            <option value='json'>JSON（one-nvr 导出）</option>
          </select>
        </label>
        <label className='grid gap-2 text-sm'>
          导入配置文件
          <input
            name='file'
            type='file'
            accept='.csv,.json'
            disabled={pending || busy}
          />
        </label>
        <Button disabled={pending || busy}>生成导入预览</Button>
      </form>
      {preview && (
        <>
          <p className='text-sm'>
            预览有效至：{new Date(preview.expires_at).toLocaleString()} · 状态：
            {progress.data?.state || 'preview'}
          </p>
          <p className='text-sm'>
            批任务编号：{preview.batch_id} ·{' '}
            <a
              href={`/channels?batch=${encodeURIComponent(preview.batch_id)}`}
              className='underline'
            >
              恢复此批任务
            </a>
          </p>
          <ImportRows items={items} />
          <div className='grid gap-3'>
            {items
              .filter(
                (item) =>
                  !['succeeded', 'skipped', 'cancelled'].includes(item.state)
              )
              .map((item) => (
                <article
                  key={item.row}
                  className='grid gap-3 rounded-lg border p-4 md:grid-cols-2'
                >
                  <label className='flex gap-2 text-sm'>
                    <input
                      type='checkbox'
                      disabled={pending || busy}
                      checked={selectedRows.includes(item.row)}
                      onChange={(e) => {
                        setRows((old) =>
                          e.target.checked
                            ? [...old, item.row]
                            : old.filter((row) => row !== item.row)
                        )
                        setConfirmation(false)
                      }}
                    />
                    选择第 {item.row} 行
                  </label>
                  <label className='grid gap-2 text-sm'>
                    第 {item.row} 行目标通道
                    <select
                      disabled={pending || busy}
                      value={selection[item.row]?.channel_id || ''}
                      onChange={(e) => {
                        const channel = channels.find(
                          (value) => value.id === e.target.value
                        )
                        if (channel)
                          update(item.row, {
                            row: item.row,
                            channel_id: channel.id,
                            expected_version: channel.version,
                          })
                      }}
                      className='rounded-md border bg-background p-2'
                    >
                      <option value=''>请选择</option>
                      {channels
                        .filter((value) =>
                          value.permissions.includes('configure')
                        )
                        .map((value) => (
                          <option key={value.id} value={value.id}>
                            CH{String(value.channel_no).padStart(2, '0')} ·{' '}
                            {value.channel_name}
                          </option>
                        ))}
                    </select>
                  </label>
                  <label className='grid gap-2 text-sm'>
                    第 {item.row} 行身份意图
                    <select
                      disabled={pending || busy}
                      value={selection[item.row]?.identity_intent || 'replace'}
                      onChange={(e) =>
                        update(item.row, {
                          identity_intent: e.target
                            .value as Selection['identity_intent'],
                        })
                      }
                      className='rounded-md border bg-background p-2'
                    >
                      <option value='modify'>修改当前摄像头配置</option>
                      <option value='replace'>更换摄像头</option>
                    </select>
                  </label>
                  <label className='grid gap-2 text-sm'>
                    第 {item.row} 行密码处理
                    <select
                      disabled={pending || busy}
                      value={selection[item.row]?.password_action || 'keep'}
                      onChange={(e) =>
                        update(item.row, {
                          password_action: e.target
                            .value as Selection['password_action'],
                        })
                      }
                      className='rounded-md border bg-background p-2'
                    >
                      <option value='keep'>保留目标已有密码</option>
                      <option value='replace'>使用文件中的密码</option>
                      <option value='clear'>清空密码</option>
                    </select>
                  </label>
                  <label className='flex gap-2 text-sm'>
                    <input
                      type='checkbox'
                      disabled={pending || busy}
                      checked={selection[item.row]?.import_name || false}
                      onChange={(e) =>
                        update(item.row, { import_name: e.target.checked })
                      }
                    />
                    第 {item.row} 行导入名称
                  </label>
                  <label className='grid gap-2 text-sm'>
                    第 {item.row} 行首次普通录像
                    <select
                      disabled={pending || busy}
                      value={
                        selection[item.row]?.first_recording_mode ||
                        'continuous'
                      }
                      onChange={(e) =>
                        update(item.row, {
                          first_recording_mode: e.target.value as
                            | 'none'
                            | 'continuous',
                        })
                      }
                      className='rounded-md border bg-background p-2'
                    >
                      <option value='continuous'>
                        开启连续录像（需已绑定存储池）
                      </option>
                      <option value='none'>关闭录像，仅取流</option>
                    </select>
                  </label>
                  {(item.state === 'failed' || item.state === 'tested') && (
                    <Button
                      type='button'
                      variant='outline'
                      disabled={
                        pending ||
                        busy ||
                        !item.channel_id ||
                        !item.expected_version
                      }
                      onClick={() =>
                        update(item.row, {
                          row: item.row,
                          channel_id: item.channel_id!,
                          expected_version: item.expected_version!,
                        })
                      }
                    >
                      重新确认第 {item.row} 行最新版本
                    </Button>
                  )}
                </article>
              ))}
          </div>
          <label className='flex gap-2 text-sm'>
            <input
              type='checkbox'
              checked={confirmation}
              disabled={pending || busy}
              onChange={(e) => setConfirmation(e.target.checked)}
            />
            已确认选中行的目标、密码处理和当前版本；应用会切换对应摄像头
          </label>
          <div className='flex flex-wrap gap-3'>
            <Button
              variant='outline'
              disabled={pending || busy || selectedRows.length === 0}
              onClick={() => void request('test')}
            >
              测试选中行
            </Button>
            <Button
              disabled={
                pending ||
                busy ||
                !confirmation ||
                selectedRows.length === 0 ||
                Date.parse(preview.expires_at) <= now
              }
              onClick={() => void request('submit')}
            >
              确认并应用选中行
            </Button>
            <Button
              variant='outline'
              disabled={
                pending ||
                busy ||
                !confirmation ||
                !items.some(
                  (item) =>
                    item.state === 'failed' && selectedRows.includes(item.row)
                )
              }
              onClick={() => void request('retry')}
            >
              重试选中失败行
            </Button>
            <Button
              variant='outline'
              disabled={pending || progress.data?.state === 'cancelled'}
              onClick={() => void request('cancel')}
            >
              取消未开始的行
            </Button>
          </div>
          {progress.error && <p role='alert'>{progress.error.message}</p>}
        </>
      )}
    </section>
  )
}
