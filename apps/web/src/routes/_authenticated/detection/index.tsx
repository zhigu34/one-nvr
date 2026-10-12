import { createFileRoute } from '@tanstack/react-router'
import { Detection } from '@/features/detection'

export const Route = createFileRoute('/_authenticated/detection/')({
  component: Detection,
})
