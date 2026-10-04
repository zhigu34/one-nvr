import { useEffect, useState } from 'react'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI, useAction, fields } from '@/features/foundation/hooks'
import {
  Panel,
  Field,
  QueryState,
  Notices,
  Empty,
} from '@/features/foundation/ui'

export function TLSSettings() {
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
  const displayTime = (value: string) =>
    new Date(value).toLocaleString('zh-CN', {
      timeZone: site.data?.timezone || 'Asia/Shanghai',
      hour12: false,
    })
  const query = useAPI<Schema<'TLSState'>>('/api/v1/settings/tls', true, 5000)
  const action = useAction()
  const state = query.data
  const active = state?.certificates.find((c) => c.id === state.active_id)
  return (
    <Panel title='访问与 SSL 证书'>
      <QueryState
        pending={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      <Notices {...action} />
      {state && (
        <>
          <div className='mb-4 grid gap-3 md:grid-cols-3'>
            <div>
              <p className='text-sm text-muted-foreground'>访问协议</p>
              <p>{state.protocol.toUpperCase()}</p>
            </div>
            <div data-testid='tls-source'>
              <p className='text-sm text-muted-foreground'>证书来源</p>
              <p>{state.source === 'directory' ? '映射目录' : '手动上传'}</p>
            </div>
            <div data-testid='tls-active'>
              <p className='text-sm text-muted-foreground'>实际生效版本</p>
              <p className='font-mono text-xs break-all'>
                {active ? active.id : '未生效'}
              </p>
            </div>
          </div>
          <p className='mb-2 text-sm'>
            应用状态：{state.state}
            {state.error_code ? ' · ' + state.error_code : ''}
          </p>
          {state.protocol === 'http' && (
            <p className='mb-4 text-sm text-muted-foreground'>
              HTTP 模式仅保存待用证书。启用 HTTPS 通过部署配置完成。
            </p>
          )}
          {state.source === 'manual' ? (
            <form
              className='mb-6 grid gap-4 md:grid-cols-3'
              onSubmit={(event) => {
                event.preventDefault()
                const form = event.currentTarget
                const data = fields(form)
                void action.run(async () => {
                  const chain = data.get('chain'),
                    key = data.get('key')
                  if (
                    !(chain instanceof File) ||
                    !(key instanceof File) ||
                    chain.size + key.size > 1024 * 1024
                  )
                    throw new Error('请选择有效 PEM 文件，组合大小不超过 1 MiB')
                  const [fullchain_pem, private_key_pem] = await Promise.all([
                    chain.text(),
                    key.text(),
                  ])
                  await apiRequest(
                    '/api/v1/settings/tls/import',
                    jsonRequest('POST', { fullchain_pem, private_key_pem })
                  )
                  form.reset()
                }, '证书已保存为待用版本')
              }}
            >
              <Field
                label='证书 fullchain.pem'
                name='chain'
                type='file'
                accept='.pem'
                required
              />
              <Field
                label='私钥 privkey.pem'
                name='key'
                type='file'
                accept='.pem'
                required
              />
              <Button className='self-end' disabled={action.pending}>
                导入证书
              </Button>
            </form>
          ) : (
            <div className='mb-6 rounded-lg border p-4'>
              <p className='mb-2 text-sm'>
                目录整目录只读映射；续签程序更新 fullchain.pem 和
                privkey.pem，完整一致后应用。
              </p>
              <p className='mb-2 text-sm'>
                输入检查：{state.check_state} · {state.check_reason} · 连续失败{' '}
                {state.consecutive_errors} 次
              </p>
              <div className='flex flex-wrap gap-3'>
                <Button
                  variant='outline'
                  disabled={action.pending}
                  onClick={() =>
                    void action.run(
                      () =>
                        apiRequest(
                          '/api/v1/settings/tls/check',
                          jsonRequest('POST')
                        ),
                      '目录检查已排队'
                    )
                  }
                >
                  立即检查目录
                </Button>
                <Button
                  variant='outline'
                  disabled={action.pending || state.state === 'applying'}
                  onClick={() =>
                    void action.run(
                      () =>
                        apiRequest(
                          '/api/v1/settings/tls/watch',
                          jsonRequest(
                            'PATCH',
                            { auto_apply: !state.auto_apply },
                            state.version
                          )
                        ),
                      state.auto_apply ? '已暂停自动应用' : '已恢复自动应用'
                    )
                  }
                >
                  {state.auto_apply ? '暂停自动应用' : '恢复自动应用'}
                </Button>
              </div>
              <p className='mt-3 text-sm text-muted-foreground'>
                自动应用：{state.auto_apply ? '已启用' : '已暂停'}
              </p>
            </div>
          )}
          <div className='mb-5 flex gap-3'>
            <Button
              variant='outline'
              disabled={
                !state.previous_id ||
                state.protocol !== 'https' ||
                state.state === 'applying' ||
                action.pending
              }
              onClick={() => {
                if (window.confirm('回滚上一证书并暂停目录自动应用？'))
                  void action.run(
                    () =>
                      apiRequest(
                        '/api/v1/settings/tls/rollback',
                        jsonRequest('POST', undefined, state.version)
                      ),
                    '回滚已排队，等待网关核验'
                  )
              }}
            >
              回滚上一证书
            </Button>
          </div>
          {state.certificates.length === 0 && <Empty>尚无已保存证书。</Empty>}
          <div className='grid gap-4'>
            {state.certificates.map((cert) => (
              <article className='rounded-lg border p-4' key={cert.id}>
                <div className='mb-3 flex flex-wrap items-center gap-3'>
                  <h3 className='font-medium'>
                    {cert.common_name || cert.subject}
                  </h3>
                  <span className='rounded-full border px-2 py-1 text-xs'>
                    {cert.id === state.active_id
                      ? '生效'
                      : cert.id === state.desired_id
                        ? '等待核验'
                        : cert.id === state.candidate_id
                          ? '待用'
                          : '历史版本'}
                  </span>
                  <Expiry certificate={cert} />
                </div>
                <dl className='grid gap-2 text-sm'>
                  <div>
                    <dt className='inline text-muted-foreground'>颁发者：</dt>
                    <dd className='inline'>{cert.issuer}</dd>
                  </div>
                  <div>
                    <dt className='inline text-muted-foreground'>SAN：</dt>
                    <dd className='inline'>{cert.sans.join(' / ')}</dd>
                  </div>
                  <div>
                    <dt className='inline text-muted-foreground'>有效期：</dt>
                    <dd className='inline'>
                      {displayTime(cert.not_before)} —{' '}
                      {displayTime(cert.not_after)}
                    </dd>
                  </div>
                  <div>
                    <dt className='inline text-muted-foreground'>算法：</dt>
                    <dd className='inline'>
                      {cert.algorithm} {cert.key_bits} ·{' '}
                      {cert.self_signed ? '自签名' : cert.chain_status}
                    </dd>
                  </div>
                  <div className='font-mono text-xs break-all'>
                    SHA256 {cert.leaf_sha256}
                  </div>
                </dl>
                <p className='my-3 text-xs text-muted-foreground'>
                  证书配对和提供链已校验；客户端是否信任取决于客户端证书库。
                </p>
                <Button
                  variant='outline'
                  disabled={
                    state.protocol !== 'https' ||
                    state.state === 'applying' ||
                    cert.id === state.active_id ||
                    action.pending
                  }
                  onClick={() =>
                    void action.run(
                      () =>
                        apiRequest(
                          `/api/v1/settings/tls/${cert.id}/apply`,
                          jsonRequest('POST', undefined, state.version)
                        ),
                      '应用已排队，等待新连接指纹核验'
                    )
                  }
                >
                  应用此版本
                </Button>
              </article>
            ))}
          </div>
        </>
      )}
    </Panel>
  )
}
function Expiry({ certificate }: { certificate: Schema<'Certificate'> }) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60000)
    return () => clearInterval(timer)
  }, [])
  const days = Math.ceil((Date.parse(certificate.not_after) - now) / 86400000)
  return days <= 30 ? (
    <span
      className={
        days <= 7 ? 'text-sm text-destructive' : 'text-sm text-amber-500'
      }
    >
      {days <= 0 ? '已过期' : `${days} 天后过期`}
    </span>
  ) : (
    <span className='text-xs text-muted-foreground'>剩余 {days} 天</span>
  )
}
