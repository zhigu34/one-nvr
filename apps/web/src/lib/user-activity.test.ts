import {expect,test,vi} from 'vitest'
import {attachUserActivity} from './user-activity'

test('only visible user interactions renew, with throttling and cleanup',()=>{
 const target=new EventTarget(),send=vi.fn(),time={now:0},view={visible:true}
 const stop=attachUserActivity(target,send,()=>time.now,()=>view.visible)
 expect(send).not.toHaveBeenCalled()
 target.dispatchEvent(new Event('pointerdown'))
 target.dispatchEvent(new Event('keydown'))
 expect(send).toHaveBeenCalledTimes(1)
 time.now=60001
 target.dispatchEvent(new Event('scroll'))
 expect(send).toHaveBeenCalledTimes(2)
 time.now=120002;view.visible=false
 target.dispatchEvent(new Event('pointerdown'))
 expect(send).toHaveBeenCalledTimes(2)
 stop();view.visible=true
 target.dispatchEvent(new Event('keydown'))
 expect(send).toHaveBeenCalledTimes(2)
})
