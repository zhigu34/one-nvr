import { writeFileSync } from 'node:fs'
import { expect, test, type Page } from '../../apps/web/tests/playwright'

const password = 'Browser-test-only-2026!'
async function login(page: Page, username = 'admin', value = password) {
  await page.goto('/sign-in')
  await page.getByLabel('用户名', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill(value)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '总览', exact: true })).toBeVisible()
}
test.describe.configure({ mode: 'serial' })

test('setupPersistsAfterReload', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { name: '初始化 one-nvr' })).toBeVisible()
  await page.getByLabel('初始化令牌').fill(process.env.ONE_NVR_E2E_SETUP_TOKEN!)
  await page.getByLabel('站点名称').fill('浏览器验收站点')
  await page.getByLabel('管理员用户名').fill('admin')
  await page.getByLabel('管理员密码').fill(password)
  await page.getByRole('button', { name: '创建站点' }).click()
  await expect(page).toHaveURL(/sign-in/)
  await login(page)
  await page.reload()
  await expect(page.getByRole('heading', { name: '总览', exact: true })).toBeVisible()
  await page.getByRole('link', { name: '通道管理', exact: true }).click()
  await expect(page.getByTestId('channel-row')).toHaveCount(16)
  await expect(page.getByText('未配置摄像头').first()).toBeVisible()
  expect(await page.evaluate(() => Object.keys(localStorage).filter(k => /auth|token|session/i.test(k)))).toEqual([])
})

test('administratorNamesPermanentSlot', async ({page}) => {
  await login(page)
  await page.goto('/channels')
  const row=page.getByTestId('channel-row').first()
  await row.getByRole('link', {name: '配置 CH01', exact: true}).click()
  // Renaming lives behind the header's edit toggle now; it still saves through
  // one PATCH and never rebuilds the media stream.
  await page.getByRole('button',{name:'编辑',exact:true}).click()
  await page.getByLabel('通道名称').fill('大门验证槽位')
  await page.getByRole('button',{name:'保存基本信息',exact:true}).click()
  await expect(page.getByText('基本信息已保存')).toBeVisible()
  await page.reload()
  await expect(page.getByRole('heading', {name: '大门验证槽位'})).toBeVisible()
  await page.goto('/channels')
  await expect(page.getByTestId('channel-row').first()).toContainText('大门验证槽位')
})

test('viewerCannotConfigureChannel', async ({ page }) => {
  await login(page)
  await page.getByRole('link', { name: '用户与权限', exact: true }).click()
  await page.getByLabel('新用户名').fill('viewer')
  await page.getByLabel('初始密码').fill(password)
  await page.getByLabel('角色', { exact: true }).click()
  await page.getByRole('option', { name: '查看者', exact: true }).click()
  await page.getByRole('button', { name: '创建账号' }).click()
  await expect(page.getByText('账号已创建')).toBeVisible()
  await page.getByRole('button', { name: 'viewer 的通道权限' }).click()
  await page.getByLabel('CH01 实时').check()
  await page.getByRole('button', { name: '保存通道权限' }).click()
  await expect(page.getByText('权限已保存')).toBeVisible()
  await page.getByRole('button', { name: '用户菜单', exact: true }).click()
  await page.getByRole('menuitem', { name: '退出登录', exact: true }).click()
  await login(page, 'viewer')
  await page.goto('/channels')
  await expect(page.getByTestId('channel-row')).toHaveCount(1)
  await expect(page.getByRole('button', { name: '保存基本信息' })).toHaveCount(0)
  await page.goto('/channels/configure')
  await expect(page.getByRole('alert')).toContainText('没有权限')
  const result = await page.evaluate(async () => {
    const me = await fetch('/api/v1/auth/me').then(r => r.json()).then(r => r.data)
    const channels = await fetch('/api/v1/channels').then(r => r.json()).then(r => r.data)
    return (await fetch(`/api/v1/channels/${channels.items[0].id}`, { method:'PATCH',headers:{'Content-Type':'application/json','X-CSRF-Token':me.csrf_token,'If-Match':'"1"'},body:JSON.stringify({channel_name:'forbidden'}) })).status
  })
  expect(result).toBe(403)
})

test('disabledAndNotImplementedModulesAreDistinct', async ({ page }) => {
  await login(page)
  await page.goto('/settings')
  const enabled = process.env.ONE_NVR_E2E_MODULES === 'yes'
  await expect(page.getByTestId('intelligence-status')).toContainText(enabled ? '尚未实现' : '功能未启用')
  await expect(page.getByTestId('cloud-status')).toContainText(enabled ? '尚未实现' : '功能未启用')
  expect(await page.evaluate(async () => {const me=await fetch('/api/v1/auth/me').then(r=>r.json()).then(r=>r.data);return (await fetch('/api/v1/detection-rules',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':me.csrf_token},body:'{}'})).status})).toBe(enabled ? 501 : 409)
})

test('tlsPendingIsNotActive', async ({ page }) => {
  await login(page)
  await page.goto('/settings')
  await expect(page.getByTestId('tls-active')).toContainText('未生效')
  if (process.env.ONE_NVR_E2E_TLS_DIR) {
    await expect(page.getByTestId('tls-source')).toContainText('映射目录')
    await expect(page.getByRole('button', {name:'导入证书'})).toHaveCount(0)
    await page.getByRole('button', {name:'立即检查目录'}).click()
    await expect(page.getByText('目录检查已排队')).toBeVisible()
    await expect(page.getByTestId('tls-active')).toContainText('未生效')
    return
  }
  await expect(page.getByTestId('tls-source')).toContainText('手动上传')
  await page.getByLabel('证书 fullchain.pem').setInputFiles(process.env.ONE_NVR_E2E_CERT!)
  await page.getByLabel('私钥 privkey.pem').setInputFiles(process.env.ONE_NVR_E2E_KEY!)
  await page.getByRole('button', { name: '导入证书' }).click()
  await expect(page.getByText('证书已保存为待用版本')).toBeVisible()
  await expect(page.getByTestId('tls-active')).toContainText('未生效')
  await expect(page.getByText('HTTP 模式仅保存待用证书')).toBeVisible()
})

test('permissionsChangeClearsStaleClientState', async ({ page, browser }) => {
  await login(page, 'viewer')
  await page.goto('/channels')
  await expect(page.getByTestId('channel-row')).toHaveCount(1)
  const adminContext=await browser.newContext()
  const adminPage=await adminContext.newPage()
  await login(adminPage)
  await adminPage.goto('/users')
  await adminPage.getByRole('button',{name:'viewer 的通道权限'}).click()
  await adminPage.getByLabel('CH01 实时').uncheck()
  await adminPage.getByRole('button',{name:'保存通道权限'}).click()
  await expect(page).toHaveURL(/sign-in/, {timeout:15000})
  await expect(page.getByTestId('channel-row')).toHaveCount(0)
  await adminContext.close()
})

test('timezoneChangesDisplayWithoutRestart', async ({ page }) => {
  await login(page)
  await page.goto('/settings')
  await page.getByLabel('时区', {exact:true}).click()
  await page.getByRole('option', {name:/America\/New_York/}).click()
  await page.getByRole('button',{name:'保存站点设置'}).click()
  await expect(page.getByText('站点设置已保存')).toBeVisible()
  await expect(page.getByTestId('site-clock')).toContainText('America/New_York')
  await page.reload()
  await expect(page.getByLabel('时区', {exact:true})).toContainText('America/New_York')
})

test('directoryPoolKeepsPendingZLMStatus', async ({page}) => {
  expect(process.env.ONE_NVR_E2E_POOL, 'requires isolated Docker pool fixture').toBe('yes')
  await login(page)
  await page.goto('/storage-pools')
  await page.getByLabel('存储池名称',{exact:true}).fill('验证池')
  await page.getByLabel('挂载目录',{exact:true}).fill('/storage/pool1')
  await page.getByRole('button',{name:'添加存储池',exact:true}).click()
  await expect(page.getByText('存储池已登记')).toBeVisible()
  await expect(page.getByText('等待摄像头连接测试')).toBeVisible()
  await page.reload()
  await expect(page.getByText('/storage/pool1',{exact:true})).toBeVisible()
})

test('passwordChangeRevokesOldSession', async ({page}) => {
  await login(page)
  await page.getByRole('button',{name:'用户菜单',exact:true}).click()
  await page.getByRole('menuitem',{name:'修改密码',exact:true}).click()
  await page.getByLabel('当前密码',{exact:true}).fill(password)
  await page.getByLabel('新密码',{exact:true}).fill('Browser-new-only-2026!')
  await page.getByRole('button',{name:'保存新密码',exact:true}).click()
  await expect(page).toHaveURL(/sign-in/)
  await login(page,'admin','Browser-new-only-2026!')
  const state=await page.evaluate(async()=>{
    const get=async(path:string)=>fetch('/api/v1/'+path).then(r=>r.json()).then(r=>r.data)
    const site=await get('site'),channels=await get('channels'),pools=await get('storage-pools')
    return {site_id:site.id,channels:channels.items.map((x:{id:string;channel_name:string})=>({id:x.id,channel_name:x.channel_name})),pool_ids:pools.items.map((x:{id:string})=>x.id)}
  })
  if(process.env.ONE_NVR_E2E_STATE_FILE) writeFileSync(process.env.ONE_NVR_E2E_STATE_FILE,JSON.stringify(state),{mode:0o600})
})
