/**
 * The detection settings a batch can change, and the vocabulary the bar and the
 * read-only rows share.
 *
 * A draft holds ONLY the fields the operator touched. `null` means "leave this
 * one alone", which is what makes applying to sixteen channels safe: an operator
 * who changes the frame rate must not reset a camera whose stream or target set
 * was deliberately different.
 */
export type Sensitivity = 'sensitive' | 'balanced' | 'strict'

export type DetectionDraft = {
  enabled: boolean | null
  stream: string | null
  fps: number | null
  sensitivity: Sensitivity | null
  labels: string[] | null
}

export function emptyDetectionDraft(): DetectionDraft {
  return { enabled: null, stream: null, fps: null, sensitivity: null, labels: null }
}

export const LABEL_TEXT: Record<string, string> = {
  person: '人', bicycle: '自行车', car: '轿车', motorcycle: '摩托车', bus: '巴士',
  truck: '卡车', cat: '猫', dog: '狗', bird: '鸟', horse: '马', sheep: '羊', cow: '牛', bear: '熊',
}

// The detector's vocabulary is broad; the bar shows the common six and keeps the
// rest one click away.
export const COMMON_LABELS = ['person', 'car', 'bicycle', 'motorcycle', 'truck', 'bus']
export const MORE_LABELS = ['cat', 'dog', 'bird', 'horse', 'sheep', 'cow', 'bear']

export const STREAM_TEXT: Record<string, string> = { sub: '子码流', main: '主码流' }
export const SENSITIVITY_TEXT: Record<Sensitivity, string> = {
  sensitive: '灵敏',
  balanced: '标准',
  strict: '严格',
}

/** One line describing what a channel currently uses, for the read-only row. */
export function configSummary(labels: string[]): string {
  if (labels.length === 0) return '未选择'
  if (labels.length <= 3) return labels.map((value) => LABEL_TEXT[value] || value).join('、')
  return `${labels.length} 类`
}
