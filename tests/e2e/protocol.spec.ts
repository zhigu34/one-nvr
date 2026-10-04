import {existsSync,writeFileSync} from 'node:fs'
import {expect,test} from '../../apps/web/tests/playwright'

test('sameBrowserCanLoginAfterHTTPSChangesToHTTP',async({page,context})=>{
 test.setTimeout(120000)
 const login=async(url:string)=>{
  await page.goto(url+'/sign-in')
  await page.getByLabel('用户名',{exact:true}).fill('admin')
  await page.getByLabel('密码',{exact:true}).fill('Browser-new-only-2026!')
  await page.getByRole('button',{name:'登录',exact:true}).click()
  await expect(page.getByRole('heading',{name:'总览',exact:true})).toBeVisible()
 }
 await login('https://gateway')
 expect((await context.cookies()).some(c=>c.secure&&c.httpOnly&&c.name.includes('one_nvr_session'))).toBe(true)
 writeFileSync('/results/protocol-ready','ready',{mode:0o600})
 await expect.poll(()=>existsSync('/results/protocol-http'),{timeout:60000,intervals:[500]}).toBe(true)
 await login('http://gateway')
 expect((await context.cookies()).some(c=>!c.secure&&c.httpOnly&&c.name==='one_nvr_session')).toBe(true)
})
