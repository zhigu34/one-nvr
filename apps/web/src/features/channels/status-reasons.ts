// Reason codes are the stable contract between the API and every surface that
// shows them, so the operator-facing wording lives in one place. Keeping it out
// of the component file also keeps fast refresh working.
export const STATUS_REASONS: Record<string, string> = {
  bitrate_unknown: '码率证据不足，等待新采样',
  pool_unavailable: '存储池不可用',
  low_space: '空间不足，已暂停录像',
  source_unavailable: '无法读取视频',
  source_frozen: '视频帧未更新',
  recording_disabled: '已关闭录像',
  recording_output_stalled: '录像长时间没有产生文件，已停止录像',
  pool_observation_stale: '存储池状态观测过期，等待重新采样',
  observation_missing: '尚未产生观测：配置已保存但从未测试过',
  observation_stale: '上一次观测已过期，需要重新测试',
  source_not_configured: '通道尚未接入摄像头',
  sub_not_configured: '未填写子流路径',
  channel_disabled: '通道已停用',
}

// "No observation yet" and "the observation expired" both arrive as `unknown`,
// which reads like a fault. Name them for what they are so the operator can
// tell "not tested" apart from "broken".
export const UNKNOWN_STATUS_LABELS: Record<string, string> = {
  observation_missing: '未测试',
  observation_stale: '观测过期',
}

export function reasonLabel(code: string | undefined) {
  if (!code) return ''
  return STATUS_REASONS[code] || code
}
