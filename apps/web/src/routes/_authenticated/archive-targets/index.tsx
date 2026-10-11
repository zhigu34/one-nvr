import { createFileRoute } from '@tanstack/react-router'
import { ArchiveTargets } from '@/features/archive-targets'

export const Route = createFileRoute('/_authenticated/archive-targets/')({
  component: ArchiveTargets,
})
