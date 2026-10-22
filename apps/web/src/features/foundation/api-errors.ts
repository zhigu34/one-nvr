import { ApiError } from '@/lib/api-client'

/**
 * The single ApiError-code → operator text map. Feature pages hand their
 * domain codes in and get one consistent fallback chain: domain code →
 * server message → generic. Shared codes (version conflicts, pool evidence)
 * live here so every page words them identically.
 */
const SHARED_CODES: Record<string, string> = {
  detection_version_conflict: '配置已被他人修改，请刷新后重试',
  channel_source_version_conflict: '该通道刚被他人修改，请刷新后重试',
  snapshot_pending: '抓拍尚未生成，稍后再看',
  snapshot_unavailable: '抓拍暂时不可读',
}

export function apiErrorText(
  error: unknown,
  codes: Record<string, string> = {}
): string {
  if (error instanceof ApiError) {
    if (codes[error.code]) return codes[error.code]
    if (SHARED_CODES[error.code]) return SHARED_CODES[error.code]
    return error.message
  }
  return error instanceof Error ? error.message : '操作失败'
}
