/**
 * A media element cannot report why a segment failed to load, so the workspace
 * asks the content endpoint for its first byte before handing the segment to
 * the player. That turns the documented error envelope into a message an
 * operator can act on instead of a bare decoding failure.
 */
export const contentPath = (segmentId: string) =>
  `/api/v1/recordings/${segmentId}/content`

export async function probeSegment(segmentId: string): Promise<string> {
  let response: Response
  try {
    response = await fetch(contentPath(segmentId), {
      headers: { Range: 'bytes=0-0' },
      credentials: 'same-origin',
      cache: 'no-store',
    })
  } catch {
    return '无法连接服务，请检查网络后重试'
  }
  if (response.ok) {
    await response.body?.cancel().catch(() => {})
    return ''
  }
  try {
    const envelope = (await response.json()) as {
      error?: { message?: string }
    }
    return envelope.error?.message || '该片段暂时无法播放'
  } catch {
    return '该片段暂时无法播放'
  }
}
