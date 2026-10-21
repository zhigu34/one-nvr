import type { Schema } from '@/lib/types'

/**
 * Channels that actually have a camera behind them.
 *
 * A fixed 16- or 32-slot site reserves every slot up front, so a site with five
 * cameras has eleven empty rows. Those rows are setup context: they belong on the
 * channel management page, where the operator goes to fill them. Everywhere else
 * they were noise — a live grid with black tiles, a playback channel picker with
 * eleven entries that can never return footage, a recording plan listing channels
 * that cannot record.
 *
 * The flag comes from the server (`Channel.configured`), computed in the same
 * query that lists the channels from `current_revision_id IS NOT NULL` — the same
 * fact the API already refuses to stream on. Deriving it in the browser from
 * summary fields meant each page could drift into its own definition.
 *
 * Channel management deliberately does NOT use this: it is the one place an empty
 * slot must remain visible, because that is where it gets filled.
 */
export function configuredChannels<T extends Pick<Schema<'Channel'>, 'configured'>>(
  items: T[]
): T[] {
  return items.filter((channel) => channel.configured)
}

/** `Channel` narrowed to the ones with a camera, for pages that assume a source. */
export function isConfigured(
  channel: Pick<Schema<'Channel'>, 'configured'> | undefined
): boolean {
  return !!channel?.configured
}
