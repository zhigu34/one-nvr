import { toast } from 'sonner'

/**
 * Success feedback for actions. Successes are transient (toast, auto-dismiss)
 * while errors stay inline next to the form — an operator must not have to
 * catch a disappearing message to learn what failed.
 *
 * e2e still finds the text: sonner renders into document.body, which is what
 * `getByText` matches against.
 */
export function notifySuccess(message: string) {
  toast.success(message, { duration: 4000 })
}
