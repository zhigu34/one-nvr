import { type ReactNode, type InputHTMLAttributes, useId } from 'react'
import { ApiError } from '@/lib/api-client'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'

export function PageTitle({
  title,
  description,
}: {
  title: string
  description: string
}) {
  return (
    <div className='mb-6'>
      <h1 className='text-2xl font-semibold tracking-tight'>{title}</h1>
      <p className='mt-2 text-sm text-muted-foreground'>{description}</p>
    </div>
  )
}
export function Field({
  label,
  name,
  ...props
}: { label: string; name: string } & InputHTMLAttributes<HTMLInputElement>) {
  const id = props.id || name
  return (
    <div className='grid gap-2'>
      <Label htmlFor={id}>{label}</Label>
      <Input {...props} id={id} name={name} />
    </div>
  )
}
/**
 * Radix refuses `<SelectItem value="">` (empty string means "no value"), so an
 * empty option value round-trips through this sentinel: '' in stays '', and
 * the option label stays visible, without teaching every call site about it.
 */
const EMPTY_SELECT_VALUE = '__empty__'
function toSentinel(value: string) {
  return value === '' ? EMPTY_SELECT_VALUE : value
}
function fromSentinel(value: string) {
  return value === EMPTY_SELECT_VALUE ? '' : value
}
export function SelectField({
  label,
  ariaLabel,
  name,
  id,
  value,
  defaultValue,
  onValueChange,
  options,
  placeholder,
  disabled,
  size,
  className,
  triggerClassName,
}: {
  label?: string
  ariaLabel?: string
  name?: string
  id?: string
  value?: string
  defaultValue?: string
  onValueChange?: (value: string) => void
  options: { value: string; label: string; disabled?: boolean }[]
  placeholder?: string
  disabled?: boolean
  size?: 'sm' | 'default'
  className?: string
  triggerClassName?: string
}) {
  // A stable fallback id keeps the label associated (and still queryable by
  // accessible name) even when the call site passes neither id nor name.
  const generatedId = useId()
  const fieldId = id || name || generatedId
  return (
    <div className={cn('grid gap-2', className)}>
      {label && <Label htmlFor={fieldId}>{label}</Label>}
      <Select
        name={name}
        value={value === undefined ? undefined : toSentinel(value)}
        defaultValue={
          defaultValue === undefined ? undefined : toSentinel(defaultValue)
        }
        onValueChange={
          onValueChange &&
          ((next) => onValueChange(fromSentinel(next)))
        }
        disabled={disabled}
      >
        <SelectTrigger
          id={fieldId}
          size={size}
          aria-label={label ? undefined : ariaLabel}
          className={cn('w-full', triggerClassName)}
        >
          <SelectValue placeholder={placeholder} />
        </SelectTrigger>
        <SelectContent>
          {options.map((option) => (
            <SelectItem
              key={option.value}
              value={toSentinel(option.value)}
              disabled={option.disabled}
            >
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}
export function CheckboxField({
  label,
  name,
  id,
  checked,
  defaultChecked,
  onCheckedChange,
  disabled,
  className,
  labelClassName,
}: {
  label: string
  name?: string
  id?: string
  checked?: boolean
  defaultChecked?: boolean
  onCheckedChange?: (checked: boolean) => void
  disabled?: boolean
  className?: string
  labelClassName?: string
}) {
  const generatedId = useId()
  const fieldId = id || name || generatedId
  return (
    <div className={cn('flex items-center gap-2', className)}>
      <Checkbox
        id={fieldId}
        name={name}
        checked={checked}
        defaultChecked={defaultChecked}
        onCheckedChange={
          onCheckedChange && ((state) => onCheckedChange(state === true))
        }
        disabled={disabled}
      />
      <Label
        htmlFor={fieldId}
        className={cn('font-normal text-sm', labelClassName)}
      >
        {label}
      </Label>
    </div>
  )
}
export function Panel({
  title,
  children,
}: {
  title: string
  children: ReactNode
}) {
  return (
    <Card className='mb-6'>
      <CardHeader>
        <CardTitle className='text-lg font-medium'>{title}</CardTitle>
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  )
}
export function Notices({ error, notice }: { error: string; notice: string }) {
  return (
    <>
      {error && (
        <Alert variant='destructive' className='mb-4'>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {notice && (
        <Alert role='status' className='mb-4'>
          <AlertDescription className='text-emerald-700 dark:text-emerald-400'>
            {notice}
          </AlertDescription>
        </Alert>
      )}
    </>
  )
}
export function QueryState({
  pending,
  error,
  retry,
}: {
  pending: boolean
  error: Error | null
  retry?: () => unknown
}) {
  if (pending)
    return (
      <p role='status' className='py-8 text-muted-foreground'>
        正在加载…
      </p>
    )
  if (error)
    return (
      <div role='alert' className='my-4 rounded-xl border p-5'>
        <p>
          {error instanceof ApiError && error.status === 403
            ? '没有权限访问此页面'
            : error.message}
        </p>
        {retry && (
          <Button variant='outline' onClick={() => retry()} className='mt-3'>
            重试
          </Button>
        )}
      </div>
    )
  return null
}
export function Forbidden() {
  return (
    <div role='alert' className='rounded-xl border p-6'>
      没有权限访问此页面
    </div>
  )
}
export function Empty({ children }: { children: ReactNode }) {
  return (
    <p className='rounded-xl border border-dashed p-8 text-center text-muted-foreground'>
      {children}
    </p>
  )
}
