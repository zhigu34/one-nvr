export type Layout = 1 | 4 | 9 | 16
export type View = { layout: Layout; channels: string[] }
export function restoreView(raw: string | null, permitted: string[]): View {
  try {
    const saved = JSON.parse(raw || '{}')
    const layout: Layout = [1, 4, 9, 16].includes(saved.layout)
      ? saved.layout
      : 4
    const channels = Array.isArray(saved.channels)
      ? [
          ...new Set<string>(
            saved.channels.filter(
              (id: unknown) => typeof id === 'string' && permitted.includes(id)
            )
          ),
        ].slice(0, 16)
      : []
    return { layout, channels }
  } catch {
    return { layout: 4, channels: [] }
  }
}
