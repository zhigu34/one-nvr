import { createFileRoute } from '@tanstack/react-router'
import { Operations } from '@/features/operations'

export const Route = createFileRoute('/_authenticated/operations/')({
  component: Operations,
})
