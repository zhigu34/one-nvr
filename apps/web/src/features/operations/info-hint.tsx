import { HelpCircle } from 'lucide-react'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

/**
 * InfoHint is the "?" affordance next to a heading.
 *
 * These pages accumulated paragraphs of explanation under every title. That text
 * is worth keeping — it is the difference between an operator trusting a figure
 * and guessing at it — but it should not be the first thing the eye lands on
 * when the reader came to look at data. The hint keeps it one hover away.
 *
 * The trigger is a real button so it is reachable by keyboard: a tooltip that
 * only opens on mouse hover is invisible to anyone navigating with Tab.
 */
export function InfoHint({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type='button'
          aria-label={label}
          className='inline-flex size-5 shrink-0 items-center justify-center rounded-full text-muted-foreground/60 transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none'
        >
          <HelpCircle aria-hidden className='size-4' />
        </button>
      </TooltipTrigger>
      <TooltipContent className='max-w-sm text-xs leading-relaxed'>
        {children}
      </TooltipContent>
    </Tooltip>
  )
}
