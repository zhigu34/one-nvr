import {readFileSync} from 'node:fs'
import {expect,test} from '../../apps/web/tests/playwright'
test('restartPreservesSiteChannelPoolAndCredentials', async ({page})=>{
 const before=JSON.parse(readFileSync(process.env.ONE_NVR_E2E_STATE_FILE!,'utf8'))
 await page.goto('/sign-in')
 await page.getByLabel('用户名',{exact:true}).fill('admin')
 await page.getByLabel('密码',{exact:true}).fill('Browser-new-only-2026!')
 await page.getByRole('button',{name:'登录',exact:true}).click()
 await expect(page.getByRole('heading',{name:'总览',exact:true})).toBeVisible()
 const after=await page.evaluate(async()=>{
  const get=async(path:string)=>fetch('/api/v1/'+path).then(r=>r.json()).then(r=>r.data)
  const site=await get('site'),channels=await get('channels'),pools=await get('storage-pools')
  return {site_id:site.id,channels:channels.items.map((x:{id:string;channel_name:string})=>({id:x.id,channel_name:x.channel_name})),pool_ids:pools.items.map((x:{id:string})=>x.id),timezone:site.timezone}
 })
 expect(after.site_id).toBe(before.site_id)
 expect(after.channels).toEqual(before.channels)
 expect(after.pool_ids).toEqual(before.pool_ids)
 expect(after.timezone).toBe('America/New_York')
 await page.goto('/channels')
 await expect(page.getByTestId('channel-row').first()).toContainText('大门验证槽位')
})
