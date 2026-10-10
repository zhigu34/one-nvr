import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'

/**
 * The write proof behind binding or enabling recording is only valid for
 * 30 seconds, so an operation that finds it stale recovers by running one real
 * pool check and retrying. The server gate itself stays untouched: the proof
 * still comes from a real camera → ZLM → MP4 write, never from inference.
 */

function pause(signal: AbortSignal | undefined, ms: number) {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason)
      return
    }
    const cancel = () => {
      clearTimeout(timer)
      reject(signal?.reason)
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', cancel)
      resolve()
    }, ms)
    signal?.addEventListener('abort', cancel, { once: true })
  })
}

export type JobSettlement =
  | { state: 'succeeded' }
  | { state: 'failed'; message: string }
  | { state: 'timeout' }

const CHECK_REASONS: Record<string, string> = {
  test_source_required: '需要先完成一路摄像头的连接测试或启用',
  zlm_media_probe_failed: '存储池真实写入校验未通过，请到存储池页查看诊断',
  observation_expired: '存储池检查已过期，请重试',
  not_observed: '还没验证过这个存储池的录像写入',
}

const OPERATION_REASONS: Record<string, string> = {
  source_unavailable: '摄像头当前不可用，未做任何更改',
  pool_unavailable: '存储池不可用：容量或写入证据不足，请查看存储池页',
  bitrate_unknown: '码率证据不足，请稍后重试',
  low_space: '存储池剩余空间不足',
  zlm_write_evidence_unavailable: '存储池写入检查未通过或已过期',
  test_source_required: '需要先完成一路摄像头的连接测试或启用',
}

/** Human-readable text for a failed background job or a rejected batch row. */
export function failureText(code: string) {
  return (
    OPERATION_REASONS[code] ||
    (code ? `操作未完成（${code}）` : '操作未完成（未知原因）')
  )
}

/**
 * Wait for a background job to settle. Queue delay plus one real media probe
 * can take a while, so the deadline is generous; the caller keeps its own
 * progress text while waiting.
 */
export async function settleJob(
  jobID: string,
  signal?: AbortSignal,
  deadlineMs = 120000
): Promise<JobSettlement> {
  const deadline = Date.now() + deadlineMs
  for (;;) {
    const job = await apiRequest<Schema<'Job'>>(`/api/v1/jobs/${jobID}`, {
      signal,
    })
    if (job.state === 'succeeded') return { state: 'succeeded' }
    if (job.state === 'failed') {
      return { state: 'failed', message: failureText(job.error_code || '') }
    }
    if (Date.now() >= deadline) return { state: 'timeout' }
    await pause(signal, 1000)
  }
}

export type PoolCheckOutcome = { ok: true } | { ok: false; message: string }

/**
 * Produce fresh write evidence for a pool through the storage check the
 * operator can also trigger by hand, then confirm it on the pool's own check
 * card — a completed check job only means an observation was stored.
 */
export async function verifyPoolWrite(
  poolID: string,
  signal?: AbortSignal
): Promise<PoolCheckOutcome> {
  const { job_id } = await apiRequest<{ job_id: string }>(
    `/api/v1/storage-pools/${poolID}/test`,
    { ...jsonRequest('POST'), signal }
  )
  const settled = await settleJob(job_id, signal)
  if (settled.state === 'failed') {
    return { ok: false, message: `存储池检查任务失败：${settled.message}` }
  }
  if (settled.state === 'timeout') {
    return {
      ok: false,
      message: '存储池检查等待超时，请稍后在存储池页查看结果',
    }
  }
  const pools = await apiRequest<PageData<Schema<'Pool'>>>(
    '/api/v1/storage-pools?limit=100',
    { signal }
  )
  const check = pools.items
    .find((pool) => pool.id === poolID)
    ?.checks.find((entry) => entry.service === 'zlm')
  if (check?.state === 'healthy') return { ok: true }
  const reason = check ? CHECK_REASONS[check.reason] : ''
  return {
    ok: false,
    message: reason
      ? `存储池写入没有通过验证：${reason}`
      : '存储池写入没有通过验证，请刷新后重试',
  }
}

/**
 * The pool that would prove a channel's writes: its own binding, or the
 * enabled default pool the server falls back to when applying the mode.
 */
export async function boundOrDefaultPool(
  channelId: string,
  signal?: AbortSignal
): Promise<string | null> {
  const status = await apiRequest<Schema<'ChannelSourceStatus'>>(
    `/api/v1/channels/${channelId}/source/status`,
    { signal }
  )
  if (status.storage_pool_id) return status.storage_pool_id
  const pools = await apiRequest<PageData<Schema<'Pool'>>>(
    '/api/v1/storage-pools?limit=100',
    { signal }
  )
  return (
    pools.items.find((pool) => pool.is_default && pool.enabled)?.id ?? null
  )
}
