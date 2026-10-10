import { readFileSync, writeFileSync } from 'node:fs'
import { expect, test, type Locator, type Page } from '../../apps/web/tests/playwright'

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
// Binding, applying and enabling recover a stale pool write proof by running a
// real check and retrying inside the same click, so wait for the accepted
// (202) response and skip the recovery-only rejection instead of failing on it.
function accept(page: Page, endpoint: string) {
  return page.waitForResponse(r => r.request().method() !== 'GET' && r.url().endsWith(endpoint), {timeout: 180000})
}
async function acceptedCommand(page: Page, endpoint: string, click: () => Promise<unknown>) {
  let pending = accept(page, endpoint)
  await click()
  for (;;) {
    const actual = await pending
    if (actual.status() === 202) {
      return (await actual.json()).data as {job_id: string}
    }
    const failed = await actual.json()
    const code = failed.error?.code || 'unknown'
    if (code !== 'zlm_write_evidence_unavailable') {
      expect(actual.status(), 'command rejected: ' + code).toBe(202)
    }
    pending = accept(page, endpoint)
  }
}
async function command(page: Page, name: string, endpoint: string) {
  return acceptedCommand(page, endpoint, () => page.getByRole('button', {name, exact: true}).click())
}
// The recording plan lists every channel, so its controls must be addressed
// through their own row.
async function rowCommand(page: Page, row: Locator, name: string, endpoint: string) {
  return acceptedCommand(page, endpoint, () => row.getByRole('button', {name, exact: true}).click())
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
// Recording a camera configuration and using it are separate steps: 「仅保存」
// only writes the revision and never touches the running source, and the test
// panel is always visible beside the fields it proves. `connect` asks for the
// one-click save-and-enable path, which is only safe while the channel records
// nothing yet.
async function saveAndTest(page: Page, ip: string, main: string, sub: string, connect = false) {
  await page.getByLabel('IP 地址', {exact: true}).fill(ip)
  await page.getByLabel('主流路径', {exact: true}).fill(main)
  await page.getByLabel('子流路径（可留空）', {exact: true}).fill(sub)
  // Credential handling sits with the credential fields, not behind a
  // collapsible section, so it is reachable without opening anything.
  await page.getByLabel('密码处理', {exact: true}).click()
  await page.getByRole('option', {name: '清空密码', exact: true}).click()
  const saved = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/source-revisions'))
  const tested = page.waitForResponse(r => r.request().method() === 'POST' && /\/source-revisions\/[^/]+\/test$/.test(new URL(r.url()).pathname))
  await page.getByRole('button', {name: connect ? '保存并启用' : '仅保存', exact: true}).click()
  const response = await saved
  if (response.status() !== 201) {
    const failed = await response.json()
    expect(response.status(), 'draft rejected: ' + (failed.error?.code || 'unknown')).toBe(201)
  }
  const revision = (await response.json()).data as {id: string}
  // Saving and proving are two explicit actions now. The panel binds to the
  // saved revision, but the command above only proves the write reached the
  // server: wait for the page to report the save before pressing 「测试取流」,
  // otherwise the click can still reach the panel bound to the previous
  // revision and the proof below would belong to the wrong configuration.
  if (!connect) {
    await expect(page.getByText('已保存，未启用；启用请点「保存并启用」')).toBeVisible({timeout: 15000})
    await page.getByRole('button', {name: '测试取流', exact: true}).click()
  }
  const testResponse = await tested
  expect(testResponse.status()).toBe(202)
  expect(new URL(testResponse.url()).pathname.endsWith('/source-revisions/' + revision.id + '/test')).toBe(true)
  const job = (await testResponse.json()).data as {job_id: string}
  await finished(page, job)
  if (connect) {
    await expect(page.getByText('摄像头已连接', {exact: true})).toBeVisible({timeout: 90000})
  } else {
    await expect(page.getByText('测试状态：通过')).toBeVisible({timeout: 15000})
  }
  return revision.id
}

test('real media UI keeps channel history through no-recording, recording, source switch and rollback', async ({page, request}) => {
  test.setTimeout(480000)
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
  await page.getByLabel('通道数量').click()
  await page.getByRole('option', {name: '32 路', exact: true}).click()
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
  const channel2 = channels.find(c => c.channel_no === 2)!
  const status = () => get<Status>(page, `channels/${channel.id}/source/status`)
  const recordings = () => get<{items: {id: string; state: string; source_revision_id: string; pool_id: string; bytes: number}[]}>(page, 'recordings?' + new URLSearchParams({channel_id: channel.id, start: new Date(Date.now() - 3600000).toISOString(), end: new Date(Date.now() + 60000).toISOString()}))
  await page.goto('/storage-pools')
  await page.getByLabel('存储池名称', {exact: true}).fill('真实媒体池')
  await page.getByLabel('挂载目录', {exact: true}).fill('/storage/pool1')
  await page.getByRole('button', {name: '添加存储池', exact: true}).click()
  await expect(page.getByText('存储池已登记')).toBeVisible()
  const pool = (await get<{items: {id: string}[]}>(page, 'storage-pools')).items[0]
  await page.goto('/channels/configure?channel=' + channel.id)
  const initial = await saveAndTest(page, fixture.camera_ip, fixture.paths[2], fixture.paths[3], true)
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
  await tile.getByLabel('CH01 通道 01 码流').click()
  await page.getByRole('option', {name: '主码流', exact: true}).click()
  await expect(tile.getByText('主流',{exact:true})).toBeVisible({timeout:30000})
  // The footer reports what the browser is actually decoding — size, direct
  // packetization (no transcode) and audio presence — from the live peer
  // statistics, so it must appear once playback has started.
  const mediaInfo = tile.getByTestId('live-media-info')
  await expect(mediaInfo).toBeVisible({timeout: 20000})
  await expect(mediaInfo).toContainText(/×\d+/)
  await expect(mediaInfo).toContainText('直通')
  // The picture box follows the decoded stream proportions (not a fixed
  // 16:9), which is what removes the black bars for non-16:9 cameras.
  await expect(tile.getByTestId('live-video-area')).toHaveAttribute('style', /aspect-ratio/)
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
  // The first bind verifies the pool with real private temporary media: no
  // write proof exists yet, and the page recovers that by running the actual
  // check inside the same click instead of asking the operator to pre-check.
  // Seeded proof rows are still never used.
  // Pool binding and the recording mode are recording-plan concerns, not part of
  // configuring the camera.
  await page.goto('/recording-plan')
  const plan = page.getByTestId('policy-row').filter({hasText: 'CH01'})
  await expect(plan).toHaveCount(1)
  await plan.getByLabel('CH01 存储池', {exact: true}).click()
  await page.getByRole('option', {name: '真实媒体池', exact: true}).click()
  await expect(plan.getByRole('button', {name: '绑定', exact: true})).toBeEnabled({timeout: 15000})
  await finished(page, await rowCommand(page, plan, '绑定', '/storage-pool'))
  await expect.poll(async () => (await status()).storage_pool_id, {timeout: 30000}).toBe(pool.id)
  await page.reload()
  const rolling = page.getByTestId('policy-row').filter({hasText: 'CH01'})
  await rolling.getByLabel('CH01 录像方式', {exact: true}).click()
  await page.getByRole('option', {name: '手动（连续录像）', exact: true}).click()
  await expect(rolling.getByRole('button', {name: '应用', exact: true})).toBeEnabled({timeout: 15000})
  await finished(page, await rowCommand(page, rolling, '应用', '/recording-policy'))
  await expect.poll(async () => (await physical()).filter(s => s.isRecordingMP4).length, {timeout: 30000}).toBe(1)
  await expect.poll(async () => (await recordings()).items.filter(s => s.state === 'ready' && s.bytes > 0).length, {timeout: 90000}).toBeGreaterThan(0)
  const historical = (await recordings()).items.find(s => s.state === 'ready')!
  expect(historical.source_revision_id).toBe(initial)
  expect(historical.pool_id).toBe(pool.id)
  // Switching the source while continuous recording is on requires fresh pool
  // write evidence; the page recovers an expired proof by running the real
  // check inside the same 测试并启用 click, so no pre-check is needed here.
  await page.goto('/channels/configure?channel=' + channel.id)
  const changed = await saveAndTest(page, fixture.camera_ip, fixture.paths[0], fixture.paths[1])
  await finished(page, await command(page, '测试并启用', '/source/apply'))
  await expect.poll(async () => (await status()).current_revision_id, {timeout: 20000}).toBe(changed)
  await expect.poll(async () => (await physical()).filter(s => s.isRecordingMP4).length, {timeout: 30000}).toBe(1)
  expect((await recordings()).items.find(s => s.id === historical.id)?.state).toBe('ready')
  // API polling can observe a committed job before the page's query refresh.
  // Reload as an operator may do, preserving optimistic conflict protection.
  await page.reload()
  const restored = await saveAndTest(page, fixture.camera_ip, fixture.paths[2], fixture.paths[3])
  await finished(page, await command(page, '测试并启用', '/source/apply'))
  await expect.poll(async () => (await status()).current_revision_id, {timeout: 20000}).toBe(restored)
  await page.reload()
  // Lose the actual candidate publisher AFTER proof, so the switch must roll back.
  // This one keeps its explicit proof first: the apply must be accepted here and
  // then fail during execution, which is a different acceptance than recovery.
  await saveAndTest(page, fixture.camera_ip, fixture.paths[0], fixture.paths[1])
  await freshPoolProof(page, pool.id)
  const stopped = await request.post('http://fixture:8557/stop-one')
  expect(stopped.status()).toBe(200)
  await finished(page, await command(page, '测试并启用', '/source/apply'), 'failed')
  await expect(page.getByText(/^执行失败：/)).toBeVisible({timeout: 15000})
  expect((await status()).current_revision_id).toBe(restored)
  await expect.poll(async () => (await physical()).filter(s => s.isRecordingMP4).length, {timeout: 30000}).toBe(1)
  // Substream recovery is periodic and rollback treats its failure as a
  // visible degradation. Establish both readable streams before testing off.
  await expect.poll(async () => (await physical()).filter(s => s.tracks.some(t => t.ready && t.frames > 0)).length, {timeout: 30000}).toBe(2)
  // Turning recording off is a recording-plan action, not part of configuring
  // the camera.
  await page.goto('/recording-plan')
  const stop = page.getByTestId('policy-row').filter({hasText: 'CH01'})
  await stop.getByLabel('CH01 录像方式', {exact: true}).click()
  await page.getByRole('option', {name: '关闭录像', exact: true}).click()
  await expect(stop.getByRole('button', {name: '应用', exact: true})).toBeEnabled({timeout: 15000})
  await finished(page, await rowCommand(page, stop, '应用', '/recording-policy'))
  await expect.poll(async () => (await physical()).some(s => s.isRecordingMP4), {timeout: 20000}).toBe(false)
  await expect.poll(async () => (await physical()).filter(s => s.tracks.some(t => t.ready && t.frames > 0)).length, {timeout: 30000}).toBe(2)
  expect((await recordings()).items.find(s => s.id === historical.id)?.state).toBe('ready')
  // The published index is browsed from the playback workspace now; the
  // acceptance block at the end covers it, so go straight to the destructive
  // source action on the channel page.
  await page.goto('/channels/configure?channel=' + channel.id)
  await page.getByRole('button', {name: '历史与诊断', exact: true}).click()
  await page.locator('summary', {hasText: '危险操作'}).click()
  await page.getByRole('button', {name: '清空摄像头配置', exact: true}).click()
  await finished(page, await command(page, '清空摄像头', '/source/clear'))
  await expect.poll(async () => (await status()).current_revision_id, {timeout: 20000}).toBeNull()
  await page.reload()
  const reconfigured = await saveAndTest(page, fixture.camera_ip, fixture.paths[2], fixture.paths[3])
  await expect(page.getByLabel('首次普通录像', {exact: true})).toHaveCount(0)
  await finished(page, await command(page, '测试并启用', '/source/apply'))
  await expect.poll(async () => (await status()).current_revision_id, {timeout: 20000}).toBe(reconfigured)
  expect((await status()).recording.state).toBe('disabled')
  expect((await recordings()).items.find(s => s.id === historical.id)?.state).toBe('ready')
  expect((await status()).channel_id).toBe(channel.id)
  // M1-C: measure the authorized byte-range read path against the real
  // published segment, through the real gateway.
  await page.goto('/recordings')
  await expect(page.getByRole('heading', {name: '录像回放', exact: true})).toBeVisible()
  await page.getByRole('button', {name: 'CH01 通道 01', exact: true}).click()
  // The index must reach the timeline; a short segment inside a 24 hour window
  // is only a few pixels wide, so playback is started from the control instead
  // of a click on the bar.
  await expect.poll(async () => page.getByRole('button', {name: /^片段 /}).count(), {timeout: 30000, message: 'the recording index must reach the timeline'}).toBeGreaterThan(0)
  const contentResponses: {range: string; status: number}[] = []
  page.on('response', response => {
    if (response.url().endsWith('/content')) contentResponses.push({range: response.request().headers()['range'] || '', status: response.status()})
  })
  await page.getByRole('button', {name: '播放首个可用片段', exact: true}).click()
  const playback = page.getByTestId('playback-video')
  await expect.poll(async () => playback.evaluate((el: HTMLVideoElement) => el.getVideoPlaybackQuality().totalVideoFrames), {timeout: 60000, message: 'the published recording must decode in the browser'}).toBeGreaterThan(2)
  const playbackFrames = await playback.evaluate((el: HTMLVideoElement) => el.getVideoPlaybackQuality().totalVideoFrames)
  const geometry = await playback.evaluate((el: HTMLVideoElement) => ({duration: el.duration, width: el.videoWidth, height: el.videoHeight}))
  expect(geometry.width).toBeGreaterThan(0)
  expect(geometry.height).toBeGreaterThan(0)
  expect(geometry.duration).toBeGreaterThan(0)
  // Playback must stream from the authorized endpoint, never a private path or blob.
  expect(contentResponses.length).toBeGreaterThan(0)
  expect(contentResponses.every(r => r.status === 200 || r.status === 206)).toBe(true)
  // Range is proven by fetching the same bytes the browser just played. The
  // digest is a plain in-page FNV-1a: the entry is plain HTTP, so crypto.subtle
  // is absent in this context by design.
  const rangeProof = await page.evaluate(async (segmentId: string) => {
    const digest = (buffer: ArrayBuffer) => {
      const bytes = new Uint8Array(buffer)
      let hash = 0x811c9dc5
      for (let index = 0; index < bytes.length; index++) {
        hash ^= bytes[index]
        hash = Math.imul(hash, 0x01000193) >>> 0
      }
      return hash.toString(16).padStart(8, '0') + ':' + bytes.length
    }
    const read = async (range?: string) => {
      const response = await fetch('/api/v1/recordings/' + segmentId + '/content', range ? {headers: {Range: range}} : undefined)
      const buffer = await response.arrayBuffer()
      return {status: response.status, acceptRanges: response.headers.get('Accept-Ranges'), contentRange: response.headers.get('Content-Range'), contentType: response.headers.get('Content-Type'), length: buffer.byteLength, digest: digest(buffer), buffer}
    }
    const full = await read()
    const from = full.length >> 2
    const to = Math.min(full.length - 1, from + 4095)
    const whole = await read('bytes=0-')
    const slice = await read(`bytes=${from}-${to}`)
    const overflow = await read(`bytes=${full.length}-`)
    return {
      total: full.length,
      full: {status: full.status, acceptRanges: full.acceptRanges, contentType: full.contentType, digest: full.digest},
      whole: {status: whole.status, contentRange: whole.contentRange, digest: whole.digest},
      slice: {status: slice.status, contentRange: slice.contentRange, length: slice.length, digest: slice.digest, expected: digest(full.buffer.slice(from, to + 1)), from, to},
      overflow: {status: overflow.status, contentRange: overflow.contentRange},
    }
  }, historical.id)
  expect(rangeProof.total).toBe(historical.bytes)
  expect(rangeProof.full.status).toBe(200)
  expect(rangeProof.full.acceptRanges).toBe('bytes')
  expect(rangeProof.full.contentType).toBe('video/mp4')
  expect(rangeProof.whole.status).toBe(206)
  expect(rangeProof.whole.contentRange).toBe(`bytes 0-${rangeProof.total - 1}/${rangeProof.total}`)
  expect(rangeProof.whole.digest).toBe(rangeProof.full.digest)
  expect(rangeProof.slice.status).toBe(206)
  expect(rangeProof.slice.contentRange).toBe(`bytes ${rangeProof.slice.from}-${rangeProof.slice.to}/${rangeProof.total}`)
  expect(rangeProof.slice.length).toBe(rangeProof.slice.to - rangeProof.slice.from + 1)
  expect(rangeProof.slice.digest).toBe(rangeProof.slice.expected)
  expect(rangeProof.overflow.status).toBe(416)
  expect(rangeProof.overflow.contentRange).toBe(`bytes */${rangeProof.total}`)
  // A viewer with playback on another channel must not be able to tell this
  // channel's segment from a segment that does not exist.
  const viewer = 'm1c-viewer-' + Date.now()
  const csrf = await page.evaluate(async () => (await (await fetch('/api/v1/auth/me')).json()).data.csrf_token as string)
  const created = await page.evaluate(async ([username, secret, token]) => {
    const response = await fetch('/api/v1/users', {method: 'POST', headers: {'Content-Type': 'application/json', 'X-CSRF-Token': token}, body: JSON.stringify({username, password: secret, role: 'viewer'})})
    return {status: response.status, data: (await response.json()).data as {id: string; version: number}}
  }, [viewer, password, csrf] as const)
  expect(created.status).toBe(201)
  const granted = await page.evaluate(async ([userId, version, otherChannel, token]) => {
    const response = await fetch('/api/v1/users/' + userId + '/channel-grants', {method: 'PUT', headers: {'Content-Type': 'application/json', 'If-Match': '"' + version + '"', 'X-CSRF-Token': token}, body: JSON.stringify({grants: [{channel_id: otherChannel, actions: ['playback']}]})})
    return response.status
  }, [created.data.id, created.data.version, channel2.id, csrf] as const)
  expect(granted).toBe(200)
  const viewerContext = await page.context().browser()!.newContext()
  let unauthorizedRead: {status: number; body: string} = {status: 0, body: ''}
  let absent: {status: number; body: string} = {status: 0, body: ''}
  try {
    const viewerPage = await viewerContext.newPage()
    await viewerPage.goto('/sign-in')
    await viewerPage.getByLabel('用户名', {exact: true}).fill(viewer)
    await viewerPage.getByLabel('密码', {exact: true}).fill(password)
    await viewerPage.getByRole('button', {name: '登录', exact: true}).click()
    await expect(viewerPage.getByRole('heading', {name: '总览', exact: true})).toBeVisible()
    const read = async (segmentId: string) => viewerPage.evaluate(async (id: string) => {
      const response = await fetch('/api/v1/recordings/' + id + '/content')
      return {status: response.status, body: await response.text()}
    }, segmentId)
    unauthorizedRead = await read(historical.id)
    absent = await read('00000000-0000-4000-8000-000000000000')
    // The workspace reflects the grant: the other channel is offered, this one
    // is absent rather than merely refusing to play.
    await viewerPage.goto('/recordings')
    await expect(viewerPage.getByRole('button', {name: 'CH02 通道 02', exact: true})).toBeVisible({timeout: 20000})
    await expect(viewerPage.getByRole('button', {name: 'CH01 通道 01'})).toHaveCount(0)
  } finally {
    await viewerContext.close()
  }
  expect(unauthorizedRead.status).toBe(404)
  expect(absent.status).toBe(404)
  expect(JSON.parse(unauthorizedRead.body).error.code).toBe('not_found')
  expect(JSON.parse(absent.body).error.code).toBe('not_found')
  expect(unauthorizedRead.body).not.toContain('ftyp')
  writeFileSync('/results/media-browser.json', JSON.stringify({actual_media: true, camera_one_click_connect: true, camera_one_click_preserves_recording: true, webrtc_decoded_frames: firstFrames, live_main_sub_switch: true, live_no_recording: true, live_unauthorized_denied: true, live_readers_released: true, live_grid_cells: 4, slots: 32, active_channels: 1, synthetic_camera_pairs: 2, no_recording_keeps_pull: true, real_ready_segment: true, switch_and_rollback: true, clear_and_reconfigure_preserves_policy: true, playback_decoded_frames: playbackFrames, playback_video_width: geometry.width, playback_range_206: rangeProof.whole.status === 206, playback_range_slice_matches: rangeProof.slice.digest === rangeProof.slice.expected, playback_range_416: rangeProof.overflow.status === 416, playback_gateway_range_passthrough: true, playback_unauthorized_404: unauthorizedRead.status === 404, playback_unknown_404: absent.status === 404, playback_no_media_leak: !unauthorizedRead.body.includes('ftyp'), channel_id: channel.id, historical_segment_id: historical.id}))
})
