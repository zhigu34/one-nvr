import type { Schema } from '@/lib/types'

export type TargetStatus = Schema<'ArchiveTargetStatus'>
export type ChannelStatus = Schema<'ChannelArchiveStatus'>

/**
 * uploadState answers the one question the target list cannot: is the archive
 * running, and if not, why.
 *
 * The four states are separate on purpose. "尚未上传" and "已同步" are different
 * facts (a target that has never been given anything is not evidence that the
 * upload path works), and a stopped archive with failures must not read as
 * merely idle — that is the shape where an operator concludes the archive is
 * fine while nothing has been uploaded for a week.
 */
export type UploadState =
  | { kind: 'idle' }
  | { kind: 'healthy' }
  | { kind: 'uploading'; count: number }
  | { kind: 'failing'; count: number; code: string }

export function uploadState(status: TargetStatus | undefined): UploadState {
  const uploading = status?.uploading ?? 0
  // A stopped copy is a copy whose job gave up while the row still says
  // 'uploading'. Counting it as progress is how an archive reads as busy for
  // days while nothing moves.
  const failed = (status?.failed ?? 0) + (status?.stopped ?? 0)
  const verified = status?.verified ?? 0
  if (uploading > 0) return { kind: 'uploading', count: uploading }
  if (failed > 0) return { kind: 'failing', count: failed, code: status?.last_error ?? '' }
  if (verified > 0) return { kind: 'healthy' }
  return { kind: 'idle' }
}

export const UPLOAD_STATE_LABEL: Record<UploadState['kind'], string> = {
  idle: '尚未上传',
  healthy: '已同步',
  uploading: '上传中',
  failing: '有失败',
}

const ERROR_TEXT: Record<string, string> = {
  upload_failed: '目标无法写入，或连接中断',
  verify_failed: '上传后校验长度不一致',
  source_unavailable: '本地文件已不在原位置',
  target_unavailable: '归档目标已停用或被移除',
  target_unauthorized: '目标拒绝当前凭据',
  archive_expired_skip: '云端保留期已过，跳过上传',
  lease_expired: '上传任务反复超时',
  copy_missing: '副本记录缺失',
  handler_failed: '上传任务异常退出',
}

/**
 * errorText names the reason behind a failure. An unmapped code is shown as-is
 * rather than swallowed: a code the page does not know is still the only clue
 * the operator has, and inventing a friendly synonym would hide which one it was.
 */
export function errorText(code: string): string {
  if (!code) return '原因未知'
  return ERROR_TEXT[code] ?? code
}

export function bytesLabel(value: number | undefined): string {
  const bytes = value ?? 0
  if (bytes <= 0) return '0 B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(0) + ' KiB'
  if (bytes < 1024 * 1024 * 1024) return (bytes / 1024 / 1024).toFixed(1) + ' MiB'
  return (bytes / 1024 / 1024 / 1024).toFixed(2) + ' GiB'
}

/**
 * pendingAge answers "is the archive keeping up" in the unit that question is
 * asked in. Null means nothing is queued, which is not the same as "queued for
 * zero minutes" — rendering it as 0 would claim a backlog exists.
 */
export function pendingAge(oldest: string | null | undefined, now: number): string | null {
  if (!oldest) return null
  const since = Date.parse(oldest)
  if (Number.isNaN(since)) return null
  const minutes = Math.floor(Math.max(0, now - since) / 60000)
  if (minutes < 1) return '刚刚入队'
  if (minutes < 60) return `最早一项等待 ${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `最早一项等待 ${hours} 小时`
  return `最早一项等待 ${Math.floor(hours / 24)} 天`
}

/**
 * coverage is how much of a channel's local footage has reached the target.
 *
 * It returns null rather than a percentage when there is nothing to cover: a
 * channel with no ready segments is not "0% archived", it is a channel with
 * nothing to archive, and a 0% bar would read as a broken archive.
 */
export function coverage(copied: number | undefined, segments: number | undefined): {
  percent: number
  copied: number
  segments: number
} | null {
  const total = segments ?? 0
  if (total <= 0) return null
  const done = Math.min(copied ?? 0, total)
  return { percent: Math.round((done / total) * 100), copied: done, segments: total }
}

export const MODE_LABEL: Record<string, string> = {
  manual: '手动',
  immediate: '实时上传',
  scheduled: '定时上传',
}
