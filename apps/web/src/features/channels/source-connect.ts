import { ApiError, apiRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import {
  boundOrDefaultPool,
  verifyPoolWrite,
} from '@/features/storage-pools/pool-check'
import { sourceCommand } from './api'

export type SaveConnection = {
  action: 'connect' | 'save'
  version: number
  progress: (message: string) => void
}
function pause(signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    signal.throwIfAborted()
    const cancel = () => {
      clearTimeout(timer)
      reject(signal.reason)
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', cancel)
      resolve()
    }, 1000)
    signal.addEventListener('abort', cancel, { once: true })
  })
}
// Queue delay plus one real media probe can take a while, so the wait is
// generous; the caller keeps its own progress text while waiting.
const PROOF_DEADLINE_MS = 180000

async function poll<T>(
  path: string,
  done: (value: T) => boolean,
  deadline: number,
  signal: AbortSignal
) {
  while (Date.now() < deadline) {
    signal.throwIfAborted()
    const value = await apiRequest<T>(path, { signal })
    signal.throwIfAborted()
    if (done(value)) return value
    await pause(signal)
  }
  throw new Error('连接等待超时，请查看通道运行状态后再重试')
}

/**
 * Run one source test and return the proof once it has finished and is still
 * usable. The one-click connect and the explicit "test and enable" action both
 * go through here, so neither can accept a proof the other would reject, and
 * neither can submit a foreign revision's or an expired one.
 */
export async function proveRevision(
  channelId: string,
  revisionId: string,
  signal: AbortSignal,
  onAccepted?: (value: Schema<'SourceChange'>) => void,
  deadline = Date.now() + PROOF_DEADLINE_MS
) {
  const started = await sourceCommand<Schema<'SourceChange'>>(
    `/api/v1/channels/${channelId}/source-revisions/${revisionId}/test`,
    {},
    undefined,
    signal
  )
  signal.throwIfAborted()
  if (!started.test_id) throw new Error('未取得连接检测任务，请重试')
  onAccepted?.(started)
  const proof = await poll<Schema<'SourceTestResult'>>(
    `/api/v1/channels/${channelId}/source-tests/${started.test_id}`,
    (value) => {
      if (value.state === 'failed' || value.state === 'cancelled')
        throw new Error(failure())
      return value.state === 'succeeded'
    },
    deadline,
    signal
  )
  if (
    proof.revision_id !== revisionId ||
    proof.main.state !== 'healthy' ||
    !proof.main.first_frame ||
    !proof.expires_at ||
    !(Date.parse(proof.expires_at) > Date.now())
  )
    throw new Error('未取得有效视频画面，请检查摄像头连接后重试')
  return proof
}
function failure(code?: string | null) {
  const reasons: Record<string, string> = {
    authorization_revoked: '没有配置此通道的权限，请重新登录或联系管理员',
    source_test_expired: '连接检测已过期，请重新保存并连接',
    rollback_failed: '连接失败，原摄像头也未恢复，请检查网络和摄像头状态',
    zlm_write_evidence_unavailable:
      '录像存储池检查未通过，请检查目录权限和剩余空间',
  }
  return (
    reasons[code || ''] ||
    '连接未完成，请检查摄像头账号、密码、IP 和码流路径，以及通道运行状态'
  )
}
/**
 * Apply a tested source revision. Applying while the channel records requires
 * a write proof that only stays valid for 30 seconds, so a rejected attempt
 * recovers by running one real pool check and retrying once — the operator
 * never has to race the window. The evidence requirement itself is unchanged.
 */
export async function applyWithPoolRecovery(
  channelId: string,
  body: Schema<'SourceApplyInput'>,
  expectedVersion: number,
  signal: AbortSignal,
  progress: (message: string) => void
) {
  const send = () =>
    sourceCommand<Schema<'SourceChange'>>(
      `/api/v1/channels/${channelId}/source/apply`,
      body,
      expectedVersion,
      signal
    )
  let verificationFailure: string
  try {
    return await send()
  } catch (e) {
    if (
      signal.aborted ||
      !(e instanceof ApiError) ||
      e.code !== 'zlm_write_evidence_unavailable'
    )
      throw e
    progress('存储池写入检查已过期，正在自动重新验证…')
    const poolID = await boundOrDefaultPool(channelId, signal)
    if (!poolID) throw e
    const verified = await verifyPoolWrite(poolID, signal)
    if (verified.ok) {
      progress('验证通过，正在继续…')
      return await send()
    }
    verificationFailure = verified.message
  }
  // Reported outside the catch so the original rejection stays the only
  // thrown value on every other path.
  throw new Error(verificationFailure)
}
// Compose the existing authorized commands; never bypass media proof, change
// an existing recording policy, or adopt a concurrently changed channel version.
export async function connectSource(
  channelId: string,
  revisionId: string,
  signal: AbortSignal,
  context: SaveConnection,
  onTest: (value: Schema<'SourceChange'>) => void
) {
  async function job(id: string) {
    return poll<Schema<'Job'>>(
      `/api/v1/jobs/${id}`,
      (value) => {
        if (value.state === 'failed') throw new Error(failure(value.error_code))
        return value.state === 'succeeded'
      },
      Date.now() + PROOF_DEADLINE_MS,
      signal
    )
  }
  context.progress('正在检测摄像头连接…')
  const proof = await proveRevision(channelId, revisionId, signal, onTest)
  const status = await apiRequest<Schema<'ChannelSourceStatus'>>(
    `/api/v1/channels/${channelId}/source/status`,
    { signal }
  )
  const unchanged = () => {
    signal.throwIfAborted()
    if (status.version !== context.version)
      throw new Error('通道配置已改变，请重新保存并连接')
  }
  unchanged()
  // Connecting brings up the stream and nothing else. The recording mode is a
  // separate decision made on the recording plan page, so this path never
  // inspects or binds a storage pool; an existing policy keeps running.
  context.progress('正在连接并启用摄像头…')
  const body: Schema<'SourceApplyInput'> = {
    revision_id: revisionId,
    test_id: proof.id,
  }
  // A channel that was never configured must state its recording intent. It
  // starts stream-only; enabling recording is a later, explicit action.
  if (status.requires_initial_recording_mode) body.first_recording_mode = 'none'
  const apply = await applyWithPoolRecovery(
    channelId,
    body,
    context.version,
    signal,
    context.progress
  )
  signal.throwIfAborted()
  await job(apply.job_id)
  const active = await apiRequest<Schema<'ChannelSourceStatus'>>(
    `/api/v1/channels/${channelId}/source/status`,
    { signal }
  )
  signal.throwIfAborted()
  if (active.current_revision_id !== revisionId)
    throw new Error('通道配置已改变，请查看当前摄像头状态')
  return proof.sub.state === 'unavailable'
    ? '摄像头已连接，子流不可用，可使用主流预览'
    : '摄像头已连接'
}
export function connectionError(error: unknown) {
  if (error instanceof ApiError && error.code === 'invalid_input')
    return '连接信息无法保存，请确认是否已有正在执行的连接任务，或重新加载当前通道后重试'
  return error instanceof Error ? error.message : '连接失败，请重试'
}
