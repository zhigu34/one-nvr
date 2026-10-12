import type { ReactNode } from 'react'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'

type ButtonVariant = 'default' | 'destructive' | 'outline' | 'secondary' | 'ghost' | 'link'
type ButtonSize = 'default' | 'sm' | 'lg' | 'icon'

/**
 * A Button that owns its busy state: spinner inside, label swap, stays
 * disabled. Every submit/apply/delete button in the app goes through this so
 * "正在保存" never has to be hand-wired per page again.
 */
export function LoadingButton({
  pending,
  pendingText,
  children,
  disabled,
  variant,
  size,
  ...props
}: {
  pending: boolean
  pendingText?: string
  children: ReactNode
  variant?: ButtonVariant
  size?: ButtonSize
} & React.ComponentProps<'button'>) {
  return (
    <Button
      type='button'
      aria-busy={pending}
      disabled={disabled || pending}
      variant={variant}
      size={size}
      {...props}
    >
      {pending && <Loader2 aria-hidden className='animate-spin' size={16} />}
      {pending && pendingText ? pendingText : children}
    </Button>
  )
}
