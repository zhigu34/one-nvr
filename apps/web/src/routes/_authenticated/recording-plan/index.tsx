import { createFileRoute } from '@tanstack/react-router'
import { RecordingPlan } from '@/features/recording-plan'

export const Route = createFileRoute('/_authenticated/recording-plan/')({
  component: RecordingPlan,
})
