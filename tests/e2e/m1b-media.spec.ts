import { readFileSync, writeFileSync } from 'node:fs'
import { expect, test, type Page } from '../../apps/web/tests/playwright'

type Status = { channel_id: string; current_revision_id: string | null; storage_pool_id: string | null; main: {state: string}; recording: {state: string} }
test.use({actionTimeout: 15000})
const password = 'Browser-test-only-2026!'
async function get<T>(page: Page, path: string): Promise<T> {
  return page.evaluate(async path => {
    const response = await fetch('/api/v1/' + path)
    if (!response.ok) throw new Error('Acceptance read failed: ' + response.status)
    return (await response.json()).data
  }, path)
}
async function command(page: Page, name: string, endpoint: string) {
  const response = page.waitForResponse(r => r.request().method() !== 'GET' && r.url().endsWith(endpoint))
  await page.getByRole('button', {name, exact: true}).click()
  const actual = await response
  if (actual.status() !== 202) {
    const failed = await actual.json()
    expect(actual.status(), 'command rejected: ' + (failed.error?.code || 'unknown')).toBe(202)
  }
  return (await actual.json()).data as {job_id: string}
}
async function finished(page: Page, job: {job_id: string}, state = 'succeeded') {
  await expect.poll(async () => (await get<{state: string}>(page, 'jobs/' + job.job_id)).state, {timeout: 90000}).toBe(state)
}
async function freshPoolProof(page: Page, poolId: string) {
  // Pool evidence expires at30s; 60s recording completions do not guarantee
  // current write proof. Use the real storage UI in another tab, retaining
  // this tab's selected tested revision and plaintext-free form state.
  const poolPage = await page.context().newPage()
  try {
    await poolPage.goto('/storage-pools')
    await finished(poolPage, await command(poolPage, '立即检查', '/storage-pools/' + poolId + '/test'))
  } finally {
    await poolPage.close()
  }
}
async function saveAndTest(page: Page, ip: string, main: string, sub: string) {
  await page.getByRole('tab', {name: '摄像头连接', exact: true}).click()
  await page.getByLabel('IP 地址', {exact: true}).fill(ip)
  await page.getByLabel('主流路径', {exact: true}).fill(main)
  await page.getByLabel('子流路径', {exact: true}).fill(sub)
  const advanced = page.locator('details').filter({has: page.locator('summary', {hasText: '高级连接设置'})})
  if (!(await advanced.evaluate(el => el.hasAttribute('open')))) await advanced.locator('summary').click()
  await page.getByLabel('密码处理', {exact: true}).selectOption('clear')
  const saved = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/source-revisions'))
  const tested = page.waitForResponse(r => r.request().method() === 'POST' && /\/source-revisions\/[^/]+\/test$/.test(new URL(r.url()).pathname))
  await page.getByRole('button', {name: '保存并测试', exact: true}).click()
  const response = await saved
  if (response.status() !== 201) {
    const failed = await response.json()
    expect(response.status(), 'draft rejected: ' + (failed.error?.code || 'unknown')).toBe(201)
  }
  const revision = (await response.json()).data as {id: string}
  const testResponse = await tested
  expect(testResponse.status()).toBe(202)
  expect(new URL(testResponse.url()).pathname.endsWith('/source-revisions/' + revision.id + '/test')).toBe(true)
  const job = (await testResponse.json()).data as {job_id: string}
  await finished(page, job)
  await expect(page.getByText('测试状态：通过')).toBeVisible({timeout: 15000})
  return revision.id
}

test('real media UI keeps channel history through no-recording, recording, source switch and rollback', async ({page, request}) => {
  test.setTimeout(420000)
  const fixtureFile = process.env.ONE_NVR_E2E_MEDIA_FIXTURE
  expect(fixtureFile, 'requires the isolated actual ZLM/browser fixture; never skip this acceptance').toBeTruthy()
  const fixture = JSON.parse(readFileSync(fixtureFile!, 'utf8')) as {camera_ip: string; paths: string[]}
  const config = readFileSync('/media-config/zlm.ini', 'utf8')
  const secret = /^secret=(.+)$/m.exec(config)![1]
  // Management responses are read only by the private test runner, never the UI/cache.
  async function physical() {
    const response = await request.get('http://zlm/index/api/getMediaList', {params: {secret, schema: 'rtsp', app: 'one_nvr'}})
    expect(response.status()).toBe(200)
    const body = await response.json()
    expect(body.code).toBe(0)
    return (body.data || []) as {stream: string; isRecordingMP4: boolean; tracks: {frames: number; ready: boolean}[]}[]
  }
  await page.goto('/')
  await page.getByLabel('初始化令牌').fill(process.env.ONE_NVR_E2E_SETUP_TOKEN!)
  await page.getByLabel('站点名称').fill('真实媒体浏览器验收')
  await page.getByLabel('管理员用户名').fill('admin')
  await page.getByLabel('管理员密码').fill(password)
  await page.getByLabel('通道数量').selectOption('32')
  await page.getByRole('button', {name: '创建站点', exact: true}).click()
  await expect(page).toHaveURL(/sign-in/)
  await page.getByLabel('用户名', {exact: true}).fill('admin')
  await page.getByLabel('密码', {exact: true}).fill(password)
  await page.getByRole('button', {name: '登录', exact: true}).click()
  await expect(page.getByRole('heading', {name: '总览', exact: true})).toBeVisible()
  const channels = (await get<{items: {id: string; channel_no: number}[]}>(page, 'channels?limit=100')).items
  expect(channels).toHaveLength(32)
  expect(new Set(channels.map(c => c.id)).size).toBe(32)
  const channel = channels.find(c => c.channel_no === 1)!
  const status = () => get<Status>(page, `channels/${channel.id}/source/status`)
  const recordings = () => get<{items: {id: string; state: string; source_revision_id: string; pool_id: string; bytes: number}[]}>(page, 'recordings?' + new URLSearchParams({channel_id: channel.id, start: new Date(Date.now() - 3600000).toISOString(), end: new Date(Date.now() + 60000).toISOString()}))
  await page.goto('/storage-pools')
  await page.getByLabel('存储池名称', {exact: true}).fill('真实媒体池')
  await page.getByLabel('挂载目录', {exact: true}).fill('/storage/pool1')
  await page.getByRole('button', {name: '添加存储池', exact: true}).click()
  await expect(page.getByText('存储池已登记')).toBeVisible()
  const pool = (await get<{items: {id: string}[]}>(page, 'storage-pools')).items[0]
  await page.goto('/channels/configure?channel=' + channel.id)
  const initial = await saveAndTest(page, fixture.camera_ip, fixture.paths[2], fixture.paths[3])
  expect((await status()).current_revision_id).toBeNull()
  await page.getByLabel('首次普通录像', {exact: true}).selectOption('none')
  await finished(page, await command(page, '应用配置', '/source/apply'))
  await expect.poll(async () => (await status()).current_revision_id, {timeout: 20000}).toBe(initial)
  await expect.poll(async () => (await status()).main.state, {timeout: 30000}).toBe('healthy')
  expect((await status()).recording.state).toBe('disabled')
  await expect.poll(async () => (await physical()).filter(s => s.tracks.some(t => t.ready && t.frames > 0)).length, {timeout: 30000}).toBe(2)
  expect((await physical()).some(s => s.isRecordingMP4)).toBe(false)
  expect((await recordings()).items).toHaveLength(0)
  // Actual browser decoding is required; an SDP answer alone is not proof.
  await page.goto('/live')
  await expect(page.getByRole('heading', {name:'实时预览',exact:true})).toBeVisible()
  const negotiation=page.waitForRequest(r=>r.method()==='POST' && new URL(r.url()).pathname===`/api/v1/channels/${channel.id}/live`)
  await page.getByRole('button',{name:'CH01 通道 01',exact:true}).click()
  const offer=(await negotiation).postDataJSON().sdp as string
  const tile=page.getByRole('region',{name:'预览 CH01 通道 01',exact:true})
  const video=tile.getByTestId('live-video')
  await expect.poll(async()=>video.evaluate((el:HTMLVideoElement)=>el.getVideoPlaybackQuality().totalVideoFrames),{timeout:45000,message:'real WebRTC decoded frames required'}).toBeGreaterThan(5)
  const firstFrames=await video.evaluate((el:HTMLVideoElement)=>el.getVideoPlaybackQuality().totalVideoFrames)
  await expect.poll(async()=>video.evaluate((el:HTMLVideoElement)=>el.getVideoPlaybackQuality().totalVideoFrames),{timeout:10000}).toBeGreaterThan(firstFrames+5)
  expect((await physical()).some(s=>s.isRecordingMP4)).toBe(false)
  const currentStreams=await physical()
  const denied=await request.post('http://zlm/index/api/whep',{params:{app:'one_nvr',stream:currentStreams[0].stream,vhost:'__defaultVhost__'},headers:{'Content-Type':'application/sdp'},data:offer})
  expect(denied.status(),'unauthorized media access must be denied').not.toBe(201)
  await tile.getByLabel('CH01 通道 01 码流').selectOption('main')
  await expect(tile.getByText('主流 · 播放中',{exact:true})).toBeVisible({timeout:30000})
  await expect.poll(async()=>video.evaluate((el:HTMLVideoElement)=>el.getVideoPlaybackQuality().totalVideoFrames),{timeout:10000}).toBeGreaterThan(5)
  await page.getByRole('button',{name:'4 画面',exact:true}).click()
  await expect(page.getByRole('region',{name:/预览 CH|空闲画面/})).toHaveCount(4)
  await page.getByRole('button',{name:'停止全部',exact:true}).click()
  await expect.poll(async()=>video.count()).toBe(0)
  await expect.poll(async()=>{
    let count=0
    for(const stream of await physical()){
      const response=await request.get('http://zlm/index/api/getMediaPlayerList',{params:{secret,schema:'rtsp',app:'one_nvr',stream:stream.stream,vhost:'__defaultVhost__'}})
      expect(response.status()).toBe(200)
      const body=await response.json();expect(body.code).toBe(0);count+=(body.data || []).length
    }
    return count
  },{timeout:20000,message:'stopping preview must remove actual media readers'}).toBe(0)
  expect((await recordings()).items).toHaveLength(0)
  // The first pool is verified by real private temporary media, never seeded proof rows.
  await page.goto('/storage-pools')
  await finished(page, await command(page, '立即检查', '/storage-pools/' + pool.id + '/test'))
  await page.goto('/channels/configure?channel=' + channel.id)
  await page.getByRole('tab', {name: '录像设置', exact: true}).click()
  await page.getByLabel('录像存储池', {exact: true}).selectOption(pool.id)
  await finished(page, await command(page, '绑定存储池', '/storage-pool'))
  await expect.poll(async () => (await status()).storage_pool_id, {timeout: 15000}).toBe(pool.id)
  await page.reload()
  await page.getByRole('tab', {name: '录像设置', exact: true}).click()
  await page.getByLabel('普通录像', {exact: true}).selectOption('continuous')
  await finished(page, await command(page, '保存录像策略', '/recording-policy'))
  await expect.poll(async () => (await physical()).filter(s => s.isRecordingMP4).length, {timeout: 30000}).toBe(1)
  await expect.poll(async () => (await recordings()).items.filter(s => s.state === 'ready' && s.bytes > 0).length, {timeout: 90000}).toBeGreaterThan(0)
  const historical = (await recordings()).items.find(s => s.state === 'ready')!
  expect(historical.source_revision_id).toBe(initial)
  expect(historical.pool_id).toBe(pool.id)
  const changed = await saveAndTest(page, fixture.camera_ip, fixture.paths[0], fixture.paths[1])
  await freshPoolProof(page, pool.id)
  await finished(page, await command(page, '应用配置', '/source/apply'))
  await expect.poll(async () => (await status()).current_revision_id, {timeout: 20000}).toBe(changed)
  // API polling can observe a committed job before the page's query refresh.
  // Reload as an operator may do, preserving optimistic conflict protection.
  await page.reload()
  const restored = await saveAndTest(page, fixture.camera_ip, fixture.paths[2], fixture.paths[3])
  await freshPoolProof(page, pool.id)
  await finished(page, await command(page, '应用配置', '/source/apply'))
  await expect.poll(async () => (await status()).current_revision_id, {timeout: 20000}).toBe(restored)
  await page.reload()
  // Lose the actual candidate publisher AFTER proof, so the switch must roll back.
  await saveAndTest(page, fixture.camera_ip, fixture.paths[0], fixture.paths[1])
  await freshPoolProof(page, pool.id)
  const stopped = await request.post('http://fixture:8557/stop-one')
  expect(stopped.status()).toBe(200)
  await finished(page, await command(page, '应用配置', '/source/apply'), 'failed')
  await expect(page.getByText(/^执行失败：/)).toBeVisible({timeout: 15000})
  expect((await status()).current_revision_id).toBe(restored)
  await expect.poll(async () => (await physical()).filter(s => s.isRecordingMP4).length, {timeout: 30000}).toBe(1)
  // Substream recovery is periodic and rollback treats its failure as a
  // visible degradation. Establish both readable streams before testing off.
  await expect.poll(async () => (await physical()).filter(s => s.tracks.some(t => t.ready && t.frames > 0)).length, {timeout: 30000}).toBe(2)
  await page.reload()
  await page.getByRole('tab', {name: '录像设置', exact: true}).click()
  await page.getByLabel('普通录像', {exact: true}).selectOption('none')
  await finished(page, await command(page, '保存录像策略', '/recording-policy'))
  await expect.poll(async () => (await physical()).some(s => s.isRecordingMP4), {timeout: 20000}).toBe(false)
  await expect.poll(async () => (await physical()).filter(s => s.tracks.some(t => t.ready && t.frames > 0)).length, {timeout: 30000}).toBe(2)
  expect((await recordings()).items.find(s => s.id === historical.id)?.state).toBe('ready')
  await page.reload()
  await page.getByRole('tab', {name: '录像设置', exact: true}).click()
  await page.getByLabel('录像结束时间', {exact: true}).fill(new Date(Date.now() + 60000).toISOString())
  await page.getByRole('button', {name: '检索录像索引', exact: true}).click()
  await expect(page.getByRole('cell', {name: '可用', exact: true}).first()).toBeVisible()
  await page.getByRole('tab', {name: '历史与诊断', exact: true}).click()
  await page.locator('summary', {hasText: '清空摄像头配置'}).click()
  await page.getByLabel('确认清空此通道摄像头', {exact: true}).check()
  await finished(page, await command(page, '清空摄像头', '/source/clear'))
  await expect.poll(async () => (await status()).current_revision_id, {timeout: 20000}).toBeNull()
  await page.reload()
  const reconfigured = await saveAndTest(page, fixture.camera_ip, fixture.paths[2], fixture.paths[3])
  await expect(page.getByLabel('首次普通录像', {exact: true})).toHaveCount(0)
  await finished(page, await command(page, '应用配置', '/source/apply'))
  await expect.poll(async () => (await status()).current_revision_id, {timeout: 20000}).toBe(reconfigured)
  expect((await status()).recording.state).toBe('disabled')
  expect((await recordings()).items.find(s => s.id === historical.id)?.state).toBe('ready')
  expect((await status()).channel_id).toBe(channel.id)
  writeFileSync('/results/media-browser.json', JSON.stringify({actual_media: true, webrtc_decoded_frames: firstFrames, live_main_sub_switch: true, live_no_recording: true, live_unauthorized_denied: true, live_readers_released: true, live_grid_cells: 4, slots: 32, active_channels: 1, synthetic_camera_pairs: 2, no_recording_keeps_pull: true, real_ready_segment: true, switch_and_rollback: true, clear_and_reconfigure_preserves_policy: true, channel_id: channel.id, historical_segment_id: historical.id}))
})
