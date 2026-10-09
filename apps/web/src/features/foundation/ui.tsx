import { type ReactNode, type InputHTMLAttributes } from 'react'
import { ApiError } from '@/lib/api-client'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

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
