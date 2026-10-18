/**
 * Formatting for the reliability figures.
 *
 * The whole point of this module is that a rate never renders as a bare
 * percentage. PRD REL-04 requires every ratio to carry its numerator,
 * denominator and unjudgeable time, and REL-05 wants the reason shown when a
 * rate has no value. Rendering "0%" for "nothing was observed" would be the
 * exact misreading those requirements exist to prevent, so the status is
 * checked before the number is ever formatted.
 */

export type RateStatus =
  | 'ok'
  | 'no_observation'
  | 'not_applicable'
  | 'no_samples'

export type Rate = {
  status: RateStatus
  numerator_ms: number
  denominator_ms: number
  unjudgeable_ms: number
}

/** Why a rate has no value, in words an operator can act on. */
export const RATE_STATUS_LABEL: Record<RateStatus, string> = {
  ok: '',
  no_observation: '无有效观测',
  not_applicable: '不适用',
  no_samples: '无样本',
}

/**
 * percentage returns the ratio only when it is meaningful, and the reason
 * otherwise. A caller cannot accidentally render a missing value as zero,
 * because a zero and a reason are different types here.
 */
export function percentage(rate: Rate): { kind: 'percent'; value: string } | { kind: 'reason'; label: string } {
  if (rate.status !== 'ok' || rate.denominator_ms <= 0) {
    return { kind: 'reason', label: RATE_STATUS_LABEL[rate.status] || rate.status }
  }
  const value = (rate.numerator_ms / rate.denominator_ms) * 100
  return { kind: 'percent', value: value.toFixed(2) + '%' }
}

/** formatDuration renders milliseconds as a compact human duration. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0 秒'
  const seconds = Math.round(ms / 1000)
  if (seconds < 60) return `${seconds} 秒`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) {
    const rest = seconds % 60
    return rest ? `${minutes} 分 ${rest} 秒` : `${minutes} 分`
  }
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  if (hours < 24) {
    return restMinutes ? `${hours} 小时 ${restMinutes} 分` : `${hours} 小时`
  }
  const days = Math.floor(hours / 24)
  const restHours = hours % 24
  return restHours ? `${days} 天 ${restHours} 小时` : `${days} 天`
}

/**
 * RateEvidence describes a rate's inputs for display beside the figure, so the
 * reader can recompute it rather than trust it.
 */
export function rateEvidence(rate: Rate): string {
  const parts = [
    `分子 ${formatDuration(rate.numerator_ms)}`,
    `分母 ${formatDuration(rate.denominator_ms)}`,
  ]
  // Unjudgeable time is deliberately NOT repeated per rate. It is one fact about
  // the window (the monitoring-unknown total), and the panel already shows it
  // once in the time-accounting row; repeating it under all three ratios made it
  // look like each had its own, and buried the numbers that do differ.
  return parts.join(' · ')
}

/**
 * PRECISION_LABEL explains which retention tier answered a window. REL-06
 * requires a statistic crossing the edge of detailed retention to state its
 * accuracy rather than imply it saw everything.
 */
export const PRECISION_LABEL: Record<string, string> = {
  exact: '精确（来自观测明细）',
  minute_rollup: '分钟汇总（明细已超出读取范围，时长准确但不可下钻到秒）',
  daily_rollup: '按日汇总（明细已过保留期，仅供趋势参考）',
  unavailable: '不可用（超出所有保留范围，无证据）',
  assumed: '策略时刻为回填值（迁移前的历史，生效时刻不确定）',
}

/** precisionNote returns the explanation, or empty when precision is exact. */
export function precisionNote(precision: string | undefined): string {
  if (!precision || precision === 'exact') return ''
  return PRECISION_LABEL[precision] || precision
}

/**
 * KIND_LABEL names the stream kinds as an operator thinks of them.
 *
 * The three are always shown separately: PRD REL-04 forbids a healthy sub
 * stream from masking an unavailable main stream, so there is no combined label
 * and no combined figure anywhere in this UI.
 */
export const KIND_LABEL: Record<string, string> = {
  main: '主流',
  sub: '子流',
  recording: '录像',
}

export const KIND_ORDER = ['main', 'sub', 'recording'] as const

/**
 * TimeBand is one coloured stretch of the availability timeline.
 */
export type TimeBand = {
  kind: string
  state: string
  exclusion?: string
  started_at: string
  ended_at?: string | null
  reason: string
}

/**
 * bandTone maps an interval to a colour token.
 *
 * Excluded time is deliberately grey rather than green: "not required to be up"
 * is neither good nor bad news, and painting it green would let a disabled
 * channel read as perfectly available.
 */
export function bandTone(band: TimeBand): string {
  if (band.exclusion || band.state === 'excluded') return 'bg-zinc-500/40'
  switch (band.state) {
    case 'healthy':
      return 'bg-emerald-500/70'
    case 'unavailable':
      return 'bg-red-500/70'
    case 'unknown':
      return 'bg-amber-500/60'
  }
  return 'bg-zinc-600/50'
}

/** BAND_LEGEND explains the tones, including the one that is not a verdict. */
export const BAND_LEGEND = [
  { tone: 'bg-emerald-500/70', label: '可用' },
  { tone: 'bg-red-500/70', label: '不可用' },
  { tone: 'bg-amber-500/60', label: '未知（证据缺口）' },
  { tone: 'bg-zinc-500/40', label: '不要求（停用/未配置/维护）' },
] as const

/**
 * bandReason explains a band in words, preferring the exclusion when one is
 * set: "disabled" is more useful than the raw reason code for excluded time.
 */
export function bandReason(band: TimeBand): string {
  if (band.exclusion) {
    switch (band.exclusion) {
      case 'disabled':
        return '已停用或明确关闭'
      case 'not_configured':
        return '未配置源'
      case 'maintenance':
        return '计划维护'
      case 'cleared':
        return '已清除配置'
    }
    return band.exclusion
  }
  return band.reason
}

/**
 * EXCLUSION_LABEL names the reasons recording was not required, so the
 * completeness panel can show them separately rather than blending them in.
 */
export const EXCLUSION_LABEL: Record<string, string> = {
  disabled: '停用/关闭',
  not_configured: '未配置',
  maintenance: '计划维护',
  cleared: '已清除',
}

/**
 * sampleNote warns when a channel has too little observation to judge.
 *
 * PRD REL-05: a channel with little history must read as insufficient rather
 * than as the most reliable one, so this returns a string whenever the evidence
 * is thin, and the UI shows it instead of a ranking.
 */
export function sampleNote(observedMs: number, windowMs: number): string {
  if (observedMs <= 0) {
    // Zero observation is not a sample-size problem, it is a "nothing was ever
    // required to run" problem. On a site where every channel has recording
    // turned off, this is the normal state of the recording rows, and calling it
    // "insufficient sample" would suggest the camera is unreliable rather than
    // switched off.
    return '窗口内没有观测记录（可能未要求运行）'
  }
  // The test is an absolute floor, not a fraction of the window.
  //
  // Requiring a percentage of the window punished the honest case: a site that
  // has been up for fourteen hours but is compared over seven days has only 8%
  // coverage and got labelled "insufficient sample" on every channel, even
  // though fourteen hours is plenty to judge a camera. The question is "do we
  // have enough evidence", which is a duration, not a proportion — a long window
  // does not make the same evidence weaker.
  const minimum = 60 * 60 * 1000
  if (observedMs < minimum) {
    return `观测时长仅 ${formatDuration(observedMs)}，样本不足，不参与可靠性比较`
  }
  // Beyond the floor, a window that is mostly unobserved still deserves a note,
  // because the figures cover far less time than the header implies.
  if (windowMs > 0 && observedMs / windowMs < 0.5) {
    return `窗口内仅 ${formatDuration(observedMs)} 有观测（占 ${((observedMs / windowMs) * 100).toFixed(0)}%），比率只代表该时段`
  }
  return ''
}

/**
 * emptyObservationReason explains a zero-observation row for the kind concerned.
 *
 * Separated from sampleNote because the two answer different questions: "the
 * sample is too small to judge" versus "there was nothing to observe because the
 * feature is off". Only the second applies to a channel whose recording mode is
 * none, which is every channel on a default install.
 */
export function emptyObservationReason(kind: string): string {
  if (kind === 'recording') return '未要求录像（模式为关闭），因此没有观测'
  return '窗口内没有任何观测记录'
}

/**
 * ALERT_KIND_LABEL names the four configurable thresholds as an operator thinks
 * of them. Shared by the rules panel and the alert list so a rule and the alert
 * it produced cannot be labelled differently.
 */
export const ALERT_KIND_LABEL: Record<string, string> = {
  main_down_streak: '主流连续不可用',
  recording_segment_timeout: '应录分片超时',
  daily_coverage_drop: '每日录像覆盖下降',
  monitoring_stopped: '监测停止',
}

/** Selectable windows. The PRD names these four plus a custom range. */
export const WINDOW_PRESETS = [
  { label: '最近 24 小时', hours: 24 },
  { label: '最近 7 天', hours: 24 * 7 },
  { label: '最近 30 天', hours: 24 * 30 },
  { label: '最近 90 天', hours: 24 * 90 },
] as const

/** windowFor turns a preset into the from/to pair the API takes. */
export function windowFor(hours: number, now = new Date()): { from: string; to: string } {
  return {
    from: new Date(now.getTime() - hours * 3600_000).toISOString(),
    to: now.toISOString(),
  }
}

/**
 * channelLabel renders the operator-facing channel number.
 *
 * It refuses to invent one. The previous inline `CH{channel_no}` printed
 * "CHundefined" for every row when the API omitted the field, which reads like a
 * real channel named "undefined" instead of a missing value. An absent number is
 * shown as an explicit placeholder so the defect is visible rather than
 * convincing.
 */
export function channelLabel(channelNo: number | null | undefined): string {
  if (typeof channelNo !== 'number' || !Number.isFinite(channelNo) || channelNo <= 0) {
    return 'CH—'
  }
  return `CH${String(channelNo).padStart(2, '0')}`
}

/**
 * Fault categories from PRD REL-02. The list is closed: the backend emits an
 * unknown category rather than inventing a ninth, so a label missing here means
 * a category was added without a translation — visible rather than blank.
 *
 * Wording follows the PRD's conservatism: "upstream_unavailable" means the source
 * or its link was unavailable, never "the camera is broken".
 */
export const FAULT_CATEGORY_LABEL: Record<string, string> = {
  upstream_unavailable: '上游不可用',
  auth_or_config: '认证或配置错误',
  recording_service: '录像服务异常',
  storage_directory: '存储目录异常',
  detection_service: '检测服务异常',
  monitoring_unknown: '监测中断',
  maintenance_or_switch: '维护或切换',
  // "unknown" here is PRD REL-02's own category: the evidence did not support
  // attributing the fault to any of the eight. "未归类" read like a labelling
  // failure on our side; "原因未定" says what actually happened.
  unknown: '原因未定',
}

export function faultCategoryLabel(category: string): string {
  return FAULT_CATEGORY_LABEL[category] ?? category
}

/**
 * durationLabel renders an outage length without producing "中断 0 秒".
 *
 * formatDuration rounds to whole seconds, so any outage under 500ms prints
 * "0 秒" — a contradiction next to a non-zero fault count, and a false statement
 * about a channel that really was unavailable. Below a second the honest label is
 * "< 1 秒"; zero stays zero because then the count beside it is also zero.
 */
export function durationLabel(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0 秒'
  if (ms < 1000) return '< 1 秒'
  return formatDuration(ms)
}
