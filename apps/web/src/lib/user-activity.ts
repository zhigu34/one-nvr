// No timer: background refreshes never extend the user's idle deadline.
export function attachUserActivity(
  target: EventTarget,
  renew: () => void,
  now: () => number = Date.now,
  visible: () => boolean = () => document.visibilityState === 'visible'
) {
  let lastSent = -Infinity
  const events = ['pointerdown', 'keydown', 'scroll']
  const activity = () => {
    const at = now()
    if (!visible() || at - lastSent < 60000) return
    lastSent = at
    renew()
  }
  for (const event of events) target.addEventListener(event, activity, {capture:true,passive:true})
  return () => {for (const event of events) target.removeEventListener(event, activity, true)}
}
