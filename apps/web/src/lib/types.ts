import type { components } from './api-types'

export type Schema<K extends keyof components['schemas']> =
  components['schemas'][K]
export type PageData<T> = { items: T[]; next_cursor: string | null }
export type SetupStatus = { initialized: boolean; csrf_token: string }
export type Capabilities = {
  frigate: Schema<'Capability'>
  cloud_archive: Schema<'Capability'>
}
