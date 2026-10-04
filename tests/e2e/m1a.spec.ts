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

test('viewerCannotConfigureChannel', async ({ page }) => {
  await login(page)
  await page.getByRole('link', { name: '用户与权限', exact: true }).click()
  await page.getByLabel('新用户名').fill('viewer')
  await page.getByLabel('初始密码').fill(password)
  await page.getByLabel('角色', { exact: true }).selectOption('viewer')
  await page.getByRole('button', { name: '创建账号' }).click()
  await expect(page.getByText('账号已创建')).toBeVisible()
  await page.getByRole('button', { name: 'viewer 的通道权限' }).click()
  await page.getByLabel('CH01 实时').check()
  await page.getByRole('button', { name: '保存通道权限' }).click()
  await expect(page.getByText('权限已保存')).toBeVisible()
  await page.getByRole('button', { name: '退出登录' }).click()
  await login(page, 'viewer')
  await page.goto('/channels')
  await expect(page.getByTestId('channel-row')).toHaveCount(1)
  await expect(page.getByRole('button', { name: '保存名称' })).toHaveCount(0)
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
  await page.getByLabel('时区', {exact:true}).selectOption('America/New_York')
  await page.getByRole('button',{name:'保存站点设置'}).click()
  await expect(page.getByText('站点设置已保存')).toBeVisible()
  await expect(page.getByTestId('site-clock')).toContainText('America/New_York')
  await page.reload()
  await expect(page.getByLabel('时区', {exact:true})).toHaveValue('America/New_York')
})

test('passwordChangeRevokesOldSession', async ({page}) => {
  await login(page)
  await page.getByRole('button',{name:'修改密码',exact:true}).click()
  await page.getByLabel('当前密码',{exact:true}).fill(password)
  await page.getByLabel('新密码',{exact:true}).fill('Browser-new-only-2026!')
  await page.getByRole('button',{name:'保存新密码',exact:true}).click()
  await expect(page).toHaveURL(/sign-in/)
  await login(page,'admin','Browser-new-only-2026!')
})
