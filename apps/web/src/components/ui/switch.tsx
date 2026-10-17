import * as React from 'react'
import * as SwitchPrimitive from '@radix-ui/react-switch'
import { cn } from '@/lib/utils'

function Switch({
  className,
  ...props
}: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      data-slot='switch'
      className={cn(
        'peer inline-flex h-[1.15rem] w-8 shrink-0 items-center rounded-full border border-transparent shadow-xs transition-all outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:bg-primary data-[state=unchecked]:bg-input dark:data-[state=unchecked]:bg-input/80',
        className
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        data-slot='switch-thumb'
        className={cn(
          // The thumb is positioned with the individual transform property and
          // a plain length, not Tailwind's translate-x utility: that utility
          // emits `translate: var(--tw-translate-x) var(--tw-translate-y)`, and
          // an unset --tw-translate-y makes the whole declaration invalid, so
          // the thumb stayed at the left edge while the track changed colour.
          // A percentage here resolves against the thumb's own width, so 100%
          // is exactly one thumb-width — the track is 2rem and the thumb 1rem,
          // which lands it flush against the right padding.
          'pointer-events-none block size-4 rounded-full bg-background ring-0 transition-transform duration-150 data-[state=checked]:translate-x-4 data-[state=unchecked]:translate-x-0 rtl:data-[state=checked]:-translate-x-4 dark:data-[state=checked]:bg-primary-foreground dark:data-[state=unchecked]:bg-foreground'
        )}
      />
    </SwitchPrimitive.Root>
  )
}

export { Switch }
