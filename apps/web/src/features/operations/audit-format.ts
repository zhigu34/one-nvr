/**
 * Audit action families and labels.
 *
 * The audit table is read by an operator answering "who changed what". Raw action
 * codes (`recording.deleted_local`) and UUIDs do not answer that, so every action
 * this application writes gets a Chinese description and a family, and the page
 * filters by family instead of asking someone to type a LIKE pattern.
 *
 * The mapping is deliberately total: unknown actions fall back to the action
 * string itself rather than to an empty cell. A new action that nobody labelled
 * is visible immediately, which is how missing labels get noticed.
 */

export type AuditFamily =
  | 'auth'
  | 'channel'
  | 'recording'
  | 'source'
  | 'storage'
  | 'event'
  | 'detection'
  | 'archive'
  | 'user'
  | 'tls'
  | 'site'
  | 'onvif'

export const AUDIT_FAMILIES: { value: string; label: string }[] = [
  { value: '', label: '全部操作' },
  { value: 'auth', label: '登录与会话' },
  { value: 'channel', label: '通道' },
  { value: 'recording', label: '录像' },
  { value: 'source', label: '视频源' },
  { value: 'storage', label: '存储' },
  { value: 'event', label: '事件' },
  { value: 'detection', label: '智能检测' },
  { value: 'archive', label: '云归档' },
  { value: 'user', label: '用户与权限' },
  { value: 'tls', label: '证书' },
  { value: 'site', label: '站点设置' },
  { value: 'onvif', label: 'ONVIF' },
]

/** Session actions whose object is the session itself, not a separate entity. */
const SESSION_ACTIONS = new Set([
  'auth.login',
  'auth.logout',
  'auth.session_revoked',
])

const ACTION_LABEL: Record<string, string> = {
  // Sessions
  'auth.login': '登录',
  'auth.logout': '退出登录',
  'auth.password_changed': '修改密码',
  'auth.session_revoked': '会话失效',

  // Channels
  'channel.renamed': '重命名通道',
  'channel.regrouped': '调整通道分组',
  'channel.enabled': '启用通道',
  'channel.disabled': '停用通道',
  'channel.quota_changed': '修改录像配额',

  // Recording
  'recording.deleted_local': '删除本地录像',
  'recording.streamed': '播放录像',
  'recording.protected': '保护录像片段',
  'recording.unprotected': '解除录像保护',
  'recording.exported': '导出录像',

  // Sources
  'source.draft_created': '保存视频源配置',
  'source.test_requested': '测试视频源',
  'source.apply_requested': '应用视频源',
  'source.pool_switch_requested': '切换存储池',
  'source.policy_apply_requested': '应用录像策略',
  'onvif.probed': 'ONVIF 探测',

  // Storage
  'storage.registered': '添加存储池',
  'storage.updated': '修改存储池',
  'storage.check_requested': '检查存储池',
  'storage.deleted': '移除存储池',

  // Users and access
  'user.created': '创建用户',
  'user.updated': '修改用户',
  'user.disabled': '停用用户',
  'user.enabled': '启用用户',
  'user.grants_changed': '修改通道权限',

  // Site, TLS, archive, detection
  'site.updated': '修改站点设置',
  'tls.bootstrap_queued': '申请网关证书',
  'tls.uploads': '更新证书',
  'archive.upload_requested': '上传录像到云端',
  'detection.updated': '修改检测配置',
}

/** actionLabel never returns an empty string: an unlabelled action stays visible. */
export function actionLabel(action: string): string {
  return ACTION_LABEL[action] ?? action
}

/**
 * actorLabel names the person behind an audit row.
 *
 * The distinction matters for reading the trail: system work (retention, pressure
 * cleanup) has no actor at all, while a deleted account leaves a dangling id.
 * Calling both "系统" would attribute automatic deletion to nobody in particular
 * and hide that a person once did it.
 */
export function actorLabel(
  actorName: string | undefined,
  actorID: string | null | undefined
): string {
  if (actorName && actorName.trim() !== '') return actorName
  if (actorID) return '已删除的账号'
  return '系统'
}

/**
 * objectLabel prefers the resolved label and never shows a bare UUID.
 *
 * One case needs the action: a login records the acting user as its object, so
 * the row read "admin | admin" in two adjacent columns. The object of a session
 * event is the session, not the person, and saying so is both accurate and
 * readable.
 */
export function objectLabel(
  label: string | undefined,
  objectType: string | undefined,
  action?: string
): string {
  if (action && SESSION_ACTIONS.has(action)) return 'Web 会话'
  if (label && label.trim() !== '') return label
  switch (objectType) {
    case 'channel':
      return '通道（已删除）'
    case 'user':
      return '用户（已删除）'
    case 'storage_pool':
      return '存储池（已删除）'
    case 'recording_segment':
      return '录像片段（已过期）'
    case 'event':
      return '事件（已过期）'
    default:
      return '—'
  }
}

/**
 * destructiveAction marks the actions an operator should never find by accident.
 *
 * They are the ones that destroy or expose evidence: deleting recordings,
 * removing a user, dropping a channel grant. The page highlights them so a wall
 * of retention deletions does not read like routine bookkeeping.
 */
export function destructiveAction(action: string): boolean {
  return (
    action === 'recording.deleted_local' ||
    action === 'user.disabled' ||
    action === 'user.grants_changed' ||
    action === 'storage.deleted' ||
    action === 'auth.session_revoked'
  )
}

/** The subset of an audit row that grouping needs. */
export interface AuditRowLike {
  created_at: string
  action: string
  actor_id?: string | null
  object_type?: string
  object_label?: string
}

export interface AuditGroup<T extends AuditRowLike> {
  key: string
  first: T
  count: number
  labels: string[]
  /** True when the rows have no actor: retention, pressure cleanup, probes. */
  systemActor: boolean
  /** Every row in the group, newest first, for the expand view. */
  items: T[]
}

/**
 * A retention or pressure sweep writes one audit row per segment, so a single
 * cleanup round appears as hundreds of identical rows sharing one timestamp.
 * That is faithful data and stays on the server, but rendering it verbatim makes
 * the trail unreadable: the configuration change an operator is looking for sits
 * underneath two thousand "deleted a recording" lines.
 *
 * Rows sharing timestamp, action, actor and object type are therefore shown as
 * one line carrying a count. The individual rows remain in the database and are
 * still returned by the API; this is presentation only, and the count keeps the
 * batch size visible rather than hiding it.
 */
export function groupAudits<T extends AuditRowLike>(items: T[]): AuditGroup<T>[] {
  const groups: AuditGroup<T>[] = []
  const index = new Map<string, AuditGroup<T>>()
  for (const item of items) {
    // Truncate to the second. A sweep writes its rows inside one transaction, so
    // they differ by microseconds; keying on the raw timestamp made every row its
    // own group and left the batch uncollapsed.
    const second = item.created_at.slice(0, 19)
    const key = [
      second,
      item.action,
      item.actor_id ?? '',
      item.object_type ?? '',
    ].join('|')
    const existing = index.get(key)
    if (existing) {
      existing.count += 1
      existing.items.push(item)
      if (existing.labels.length < 3 && item.object_label) {
        existing.labels.push(item.object_label)
      }
      continue
    }
    const group: AuditGroup<T> = {
      key,
      first: item,
      count: 1,
      labels: item.object_label ? [item.object_label] : [],
      systemActor: !item.actor_id,
      items: [item],
    }
    index.set(key, group)
    groups.push(group)
  }
  return groups
}
