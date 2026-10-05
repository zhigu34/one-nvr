import type { Schema } from '@/lib/types'

const states: Record<Schema<'ImportItem'>['state'], string> = {
  draft: '待确认',
  testing: '正在测试',
  tested: '测试通过',
  queued: '排队中',
  running: '正在应用',
  succeeded: '应用成功',
  skipped: '已跳过',
  failed: '失败',
  cancelled: '已取消',
}
const reasons: Record<string, string> = {
  channel_version_changed: '通道配置已改变，请重新确认',
  source_test_failed: '取流测试失败',
  empty_source_skipped: '空源行已跳过，保留原配置',
  authorization_revoked: '配置权限已撤销',
  import_row_invalid: '请检查配置与身份选择',
  credential_unavailable: '无法解密凭据，请检查持久密钥',
  duplicate_source_address: '多个通道使用同一地址',
  foreign_identity_ignored: '跨站配置已映射到本地通道',
  metadata_ignored: '已忽略文件中的策略或权限',
  cancelled: '已取消',
  name_update_conflict: '名称发生冲突，已保留最新名称',
}
export function ImportRows({ items }: { items: Schema<'ImportItem'>[] }) {
  return (
    <div className='grid gap-3'>
      {items.map((item) => (
        <article key={item.row} className='rounded-lg border p-3'>
          <p className='font-medium'>
            第 {item.row} 行 · {states[item.state]}
          </p>
          {item.differences.length > 0 && (
            <p className='text-xs text-muted-foreground'>
              变化：
              {item.differences
                .map(
                  (value) =>
                    ({
                      channel_name: '名称',
                      source_configuration: '取流配置',
                      new_source: '首次配置',
                    })[value] || value
                )
                .join(' / ')}
            </p>
          )}
          {[...item.errors, ...item.warnings].map((value, i) => (
            <p key={`${value}:${i}`} className='text-sm'>
              {reasons[value] || value}
            </p>
          ))}
        </article>
      ))}
    </div>
  )
}
