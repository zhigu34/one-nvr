import { ApiError, apiRequest, jsonRequest, newRequestKey } from '@/lib/api-client'
import type { Schema } from '@/lib/types'

/** PRD EXP-01: one task exports at most thirty minutes. */
export const EXPORT_WINDOW_LIMIT_MS = 30 * 60_000

type Analysis = Schema<'RecordingExportAnalysis'>
type Created = Schema<'RecordingExportCreated'>

/** Operator-facing text for every failure code the backend can return. */
export function exportErrorText(error: unknown): string {
  if (error instanceof ApiError) {
    const texts: Record<string, string> = {
      window_too_long: '单次导出不超过 30 分钟，请缩小时间范围',
      window_invalid: '导出窗口无效',
      export_no_source: '该时间范围没有可导出的已发布录像',
      export_too_fragmented: '该范围分段过多，请缩小时间范围',
      pool_required: '该通道未绑定存储池，请先在录像计划中绑定',
      pool_unknown: '导出输出池不可用',
      low_space: '输出池剩余空间不足',
      capacity_unknown: '输出池容量未知，请先检查存储池后重试',
      idempotency_conflict: '请求正在处理中，请刷新导出记录查看',
      export_not_ready: '导出尚未完成，暂无可下载的产物',
      export_missing: '导出任务不存在',
    }
    if (texts[error.code]) return texts[error.code]
    return error.message
  }
  return error instanceof Error ? error.message : '操作失败'
}

/** Worker-side failure codes, for the drawer's failure line. */
export function exportFailureText(code: string): string {
  const texts: Record<string, string> = {
    source_unavailable: '源录像在导出期间不可用',
    media_incompatible: '所选片段编码参数不一致，无法原码拼接',
    low_space: '输出池剩余空间不足',
    output_invalid: '导出产物校验未通过',
    job_exhausted: '导出任务多次失败，已停止重试',
  }
  return texts[code] || code
}

export function analyzeExport(
  channelID: string,
  start: number,
  end: number,
  signal?: AbortSignal
): Promise<Analysis> {
  return apiRequest<Analysis>(
    `/api/v1/recording-exports/analyze?${new URLSearchParams({
      channel_id: channelID,
      start: String(start),
      end: String(end),
    })}`,
    { signal }
  )
}

export function createExport(input: {
  channelID: string
  start: number
  end: number
  gapMode: 'merge' | 'split'
  poolID?: string
}): Promise<Created> {
  const init = jsonRequest('POST', {
    channel_id: input.channelID,
    start: input.start,
    end: input.end,
    gap_mode: input.gapMode,
    ...(input.poolID ? { pool_id: input.poolID } : {}),
  })
  // A transport retry must never produce a second task: every submission
  // carries a fresh idempotency key, and a replay resolves to the first task.
  return apiRequest<Created>('/api/v1/recording-exports', {
    ...init,
    headers: { ...init.headers, 'Idempotency-Key': newRequestKey() },
  })
}
