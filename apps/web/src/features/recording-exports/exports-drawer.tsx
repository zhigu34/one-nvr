import { useQueryClient } from '@tanstack/react-query'
import { Download, ShieldCheck, ShieldOff } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useAPI } from '@/features/foundation/hooks'
import { Empty } from '@/features/foundation/ui'
import { formatMinute } from '@/features/playback/zone'
import { exportFailureText } from './api'

type Export = Schema<'RecordingExport'>

// Export timestamps arrive as RFC3339 strings; the site-zone helper speaks in
// epoch milliseconds, so this adapter is the only conversion point.
function formatInstant(value: string | undefined, zone: string): string {
  if (!value) return '—'
  const ms = Date.parse(value)
  return Number.isFinite(ms) ? formatMinute(ms, zone) : '—'
}

const EXPORT_STATE_LABELS: Record<NonNullable<Export['state']>, string> = {
  queued: '排队中',
  running: '导出中',
  succeeded: '已完成',
  failed: '失败',
}

/**
 * FILE-06's drawer: every export of one channel with its progress, failure
 * reason, expiry and downloads. It polls while open so a running task's
 * progress moves without the operator refreshing.
 */
export function ExportsDrawer({
  channel,
  zone,
  open,
  onOpenChange,
}: {
  channel: Schema<'Channel'>
  zone: string
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const client = useQueryClient()
  const exports = useAPI<{ items: Export[] }>(
    `/api/v1/recording-exports?channel_id=${channel.id}&limit=20`,
    open,
    2000
  )
  const items = exports.data?.items || []

  async function toggleProtection(exportID: string, artifactID: string, protectedNext: boolean) {
    const init = jsonRequest('PATCH', { protected: protectedNext })
    await apiRequest(`/api/v1/recording-exports/${exportID}/artifacts/${artifactID}`, init)
    await client.invalidateQueries()
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className='flex flex-col gap-4 overflow-y-auto sm:max-w-md'>
        <SheetHeader>
          <SheetTitle>
            导出记录（CH{String(channel.channel_no).padStart(2, '0')} {channel.channel_name}）
          </SheetTitle>
          <SheetDescription>
            产物默认保留 7 天；受保护的产物不受到期清理影响。
          </SheetDescription>
        </SheetHeader>
        {exports.error && (
          <p role='alert' className='text-sm text-destructive'>
            {exports.error.message}
          </p>
        )}
        {!items.length && !exports.isPending && <Empty>还没有导出记录。</Empty>}
        <div className='space-y-3'>
          {items.map((item) => (
            <div key={item.id} className='space-y-2 rounded-xl border p-3 text-sm' data-testid='export-row'>
              <div className='flex items-center justify-between'>
                <span className={item.state === 'failed' ? 'font-medium text-destructive' : 'font-medium'}>
                  {EXPORT_STATE_LABELS[item.state ?? 'queued']}
                  {item.state === 'succeeded' ? ` · ${item.gap_mode === 'merge' ? '合并' : '分段'}` : ''}
                </span>
                <span className='text-xs text-muted-foreground'>
                  {formatInstant(item.created_at, zone)} · {item.requested_by_name}
                </span>
              </div>
              <p className='text-muted-foreground'>
                {formatInstant(item.requested_start, zone)} — {formatInstant(item.requested_end, zone)}
                {item.actual_start && item.actual_end
                  ? ` · 实际 ${formatInstant(item.actual_start, zone)} — ${formatInstant(item.actual_end, zone)}`
                  : ''}
              </p>
              {item.state === 'queued' || item.state === 'running' ? (
                <p role='status'>
                  进度 {item.progress ?? 0}%（{item.message}）
                </p>
              ) : null}
              {item.state === 'failed' && (
                <p role='alert' className='text-destructive'>
                  失败原因：{item.error_code ? exportFailureText(item.error_code ?? '') : item.message}
                </p>
              )}
              {item.state === 'succeeded' && (
                <div className='space-y-2'>
                  {(item.artifacts ?? []).map((artifact) => (
                    <div key={artifact.id} className='flex flex-wrap items-center gap-2'>
                      {artifact.state === 'ready' ? (
                        <>
                          <Button asChild size='sm' variant='outline'>
                            <a href={`/api/v1/recording-exports/${item.id}/artifacts/${artifact.id}/content`}>
                              <Download size={14} />
                              {artifact.file_name}
                            </a>
                          </Button>
                          <span className='text-xs text-muted-foreground'>
                            {((artifact.size_bytes ?? 0) / 1048576).toFixed(1)} MiB · 到期{' '}
                            {formatInstant(artifact.expires_at, zone)}
                            {artifact.protected ? ' · 已保护' : ''}
                          </span>
                          {admin && (
                            <Button
                              size='sm'
                              variant='ghost'
                              aria-label={artifact.protected ? `解除保护 ${artifact.file_name}` : `保护 ${artifact.file_name}`}
                              onClick={() => {
                                void toggleProtection(item.id ?? '', artifact.id ?? '', !artifact.protected)
                              }}
                            >
                              {artifact.protected ? <ShieldOff size={14} /> : <ShieldCheck size={14} />}
                              {artifact.protected ? '解除保护' : '保护'}
                            </Button>
                          )}
                        </>
                      ) : (
                        <span className='text-xs text-muted-foreground'>
                          {artifact.file_name} · 已到期清理
                        </span>
                      )}
                    </div>
                  ))}
                  <Button asChild size='sm'>
                    <a href={`/api/v1/recording-exports/${item.id}/download`}>
                      <Download size={14} />
                      下载 ZIP（含证据清单）
                    </a>
                  </Button>
                </div>
              )}
            </div>
          ))}
        </div>
      </SheetContent>
    </Sheet>
  )
}
