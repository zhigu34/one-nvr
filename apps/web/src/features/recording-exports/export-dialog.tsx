import { useEffect, useState } from 'react'
import { Download } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useAPI } from '@/features/foundation/hooks'
import { SelectField } from '@/features/foundation/ui'
import { formatMinute } from '@/features/playback/zone'
import {
  analyzeExport,
  createExport,
  exportErrorText,
  exportFailureText,
  EXPORT_WINDOW_LIMIT_MS,
} from './api'

type Analysis = Schema<'RecordingExportAnalysis'>
type ExportView = Schema<'RecordingExport'>

/**
 * The export dialog behind the playback page's range selection. It previews
 * the range first (EXP-02's analysis is a separate read-only step), lets the
 * operator choose how gaps are handled, then tracks the created task to its
 * terminal state and offers the downloads.
 */
export function ExportDialog({
  channel,
  zone,
  start,
  end,
  open,
  onOpenChange,
}: {
  channel: Schema<'Channel'>
  /** The site IANA zone, as everywhere else on the page. */
  zone: string
  start: number
  end: number
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const pools = useAPI<PageData<Schema<'Pool'>>>(
    '/api/v1/storage-pools?limit=100',
    admin && open
  )
  const [analysis, setAnalysis] = useState<Analysis | null>(null)
  const [analysisError, setAnalysisError] = useState('')
  const [gapMode, setGapMode] = useState<'merge' | 'split'>('merge')
  const [poolID, setPoolID] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [exportID, setExportID] = useState('')
  const view = useAPI<ExportView>(
    `/api/v1/recording-exports/${exportID}`,
    !!exportID,
    // Polling lives exactly as long as the dialog: the parent unmounts it on
    // close, which is also what stops the refetches after a terminal state.
    exportID ? 1000 : false
  )
  // Runs once per mount: the playback page remounts this dialog for every
  // selection, so the analysis states start fresh without a synchronous reset.
  useEffect(() => {
    const controller = new AbortController()
    analyzeExport(channel.id, start, end, controller.signal)
      .then(setAnalysis)
      .catch((cause) => {
        if (!controller.signal.aborted) setAnalysisError(exportErrorText(cause))
      })
    return () => controller.abort()
  }, [channel.id, start, end])

  const tooLong = end - start > EXPORT_WINDOW_LIMIT_MS
  const blocked = !analysis || tooLong || submitting || !analysis.media_consistent
  const busy =
    !!view.data && (view.data.state === 'queued' || view.data.state === 'running')
  const label = `CH${String(channel.channel_no).padStart(2, '0')} ${channel.channel_name}`

  function submit() {
    if (!analysis) return
    setSubmitting(true)
    setError('')
    createExport({
      channelID: channel.id,
      start,
      end,
      gapMode,
      poolID: poolID || undefined,
    })
      .then((created) => {
        setExportID(created.export_id ?? '')
      })
      .catch((cause) => setError(exportErrorText(cause)))
      .finally(() => setSubmitting(false))
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-lg'>
        <DialogHeader>
          <DialogTitle>导出录像（{label}）</DialogTitle>
          <DialogDescription>
            {formatMinute(start, zone)} — {formatMinute(end, zone)}
          </DialogDescription>
        </DialogHeader>
        {tooLong && (
          <p role='alert' className='text-sm text-amber-600'>
            单次导出不超过 30 分钟，请缩小时间范围后重试。
          </p>
        )}
        {error && (
          <p role='alert' className='text-sm text-destructive'>
            {error}
          </p>
        )}
        {!exportID && (
          <>
            {analysisError ? (
              <p role='alert' className='text-sm text-destructive'>
                {analysisError}
              </p>
            ) : !analysis ? (
              <p role='status' className='py-4 text-sm text-muted-foreground'>
                正在分析该时间范围…
              </p>
            ) : (
              <div className='space-y-3 text-sm'>
                <div className='grid grid-cols-2 gap-2 rounded-lg border bg-muted/40 p-3'>
                  <span className='text-muted-foreground'>可导出录像</span>
                  <span>
                    {Math.round((analysis.coverage_ms ?? 0) / 60000)} 分钟 ·{' '}
                    {((analysis.ready_bytes ?? 0) / 1048576).toFixed(1)} MiB
                  </span>
                  <span className='text-muted-foreground'>缺口</span>
                  <span>
                    {(analysis.gaps ?? []).length
                      ? `${(analysis.gaps ?? []).length} 处（共 ${Math.round((analysis.gaps ?? []).reduce((total, gap) => total + (gap.duration_ms ?? 0), 0) / 60000)} 分钟）`
                      : '无'}
                  </span>
                  <span className='text-muted-foreground'>暂不可用片段</span>
                  <span>{(analysis.unavailable ?? []).length || '无'}</span>
                </div>
                {!analysis.media_consistent && (
                  <p role='alert' className='text-sm text-amber-600'>
                    所选片段编码参数不一致，原码拼接导出将被拒绝。请缩小范围到单一参数区间。
                  </p>
                )}
                <SelectField
                  label='缺口处理'
                  value={gapMode}
                  onValueChange={(next) => setGapMode(next as 'merge' | 'split')}
                  options={[
                    { value: 'merge', label: '合并：压缩缺口，产出单一文件' },
                    { value: 'split', label: '分段：每个连续区间一个文件' },
                  ]}
                />
                {admin && (
                  <SelectField
                    label='输出存储池'
                    value={poolID}
                    onValueChange={(next) => setPoolID(next ?? '')}
                    options={[
                      { value: '', label: '通道绑定的存储池' },
                      ...(pools.data?.items || [])
                        .filter((pool) => pool.enabled)
                        .map((pool) => ({ value: pool.id, label: pool.name })),
                    ]}
                  />
                )}
              </div>
            )}
          </>
        )}
        {exportID && view.data && (
          <div className='space-y-2 text-sm'>
            {busy && (
              <p role='status'>
                导出中：{view.data.progress}%（{view.data.message}）
              </p>
            )}
            {view.data.state === 'succeeded' && (
              <div className='space-y-2'>
                <p>导出完成，产物默认保留 7 天。</p>
                <div className='flex flex-wrap gap-2'>
                  <Button asChild size='sm'>
                    <a href={`/api/v1/recording-exports/${exportID}/download`}>
                      <Download size={16} />
                      下载 ZIP（含证据清单）
                    </a>
                  </Button>
                  {(view.data.artifacts ?? [])
                    .filter((artifact) => artifact.state === 'ready')
                    .map((artifact) => (
                      <Button key={artifact.id} asChild size='sm' variant='outline'>
                        <a
                          href={`/api/v1/recording-exports/${exportID}/artifacts/${artifact.id}/content`}
                        >
                          {artifact.file_name}
                        </a>
                      </Button>
                    ))}
                </div>
              </div>
            )}
            {view.data.state === 'failed' && (
              <p role='alert' className='text-sm text-destructive'>
                导出失败：
                {view.data.error_code
                  ? exportFailureText(view.data.error_code)
                  : view.data.message}
              </p>
            )}
          </div>
        )}
        <DialogFooter>
          <Button variant='outline' onClick={() => onOpenChange(false)}>
            {exportID ? '关闭' : '取消'}
          </Button>
          {!exportID && (
            <Button onClick={submit} disabled={blocked}>
              开始导出
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
