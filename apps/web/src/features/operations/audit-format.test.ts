import { describe, expect, test } from 'vitest'
import {
  actionLabel,
  actorLabel,
  destructiveAction,
  groupAudits,
  objectLabel,
} from './audit-format'

describe('actionLabel', () => {
  test('translates the actions this application writes', () => {
    expect(actionLabel('auth.login')).toBe('登录')
    expect(actionLabel('recording.deleted_local')).toBe('删除本地录像')
    expect(actionLabel('channel.quota_changed')).toBe('修改录像配额')
  })

  // The fallback is the raw code on purpose: an unlabelled action has to be
  // visible in the table, otherwise a new action ships silently unreadable.
  test('leaves an unknown action visible rather than blank', () => {
    expect(actionLabel('something.new')).toBe('something.new')
  })
})

describe('actorLabel', () => {
  test('shows the username, not the id', () => {
    expect(actorLabel('admin', '7b6fdf36-0a04-4c61-ba4e-5757d0e6f2d9')).toBe(
      'admin'
    )
  })

  // Automatic work has no user behind it. Attributing retention deletions to a
  // person would be wrong; the distinction has to survive to the screen.
  test('names system work as 系统 when there is no actor at all', () => {
    expect(actorLabel('', null)).toBe('系统')
    expect(actorLabel(undefined, undefined)).toBe('系统')
  })

  // A deleted account leaves the id behind. That is different from "system":
  // a person did this, and the trail should say the account is gone rather than
  // pretend nobody did it.
  test('distinguishes a removed account from system work', () => {
    expect(actorLabel('', '7b6fdf36-0a04-4c61-ba4e-5757d0e6f2d9')).toBe(
      '已删除的账号'
    )
  })
})

describe('objectLabel', () => {
  test('prefers the resolved label', () => {
    expect(objectLabel('CH08 测试', 'channel')).toBe('CH08 测试')
    expect(objectLabel('2026-10-11 04:49 的录像', 'recording_segment')).toBe(
      '2026-10-11 04:49 的录像'
    )
  })

  // An object that no longer exists still gets a name, because the audit row
  // outlives the thing it describes.
  test('names the type when the object is gone', () => {
    expect(objectLabel('', 'channel')).toBe('通道（已删除）')
    expect(objectLabel('', 'recording_segment')).toBe('录像片段（已过期）')
  })

  test('never renders a bare UUID or an empty cell', () => {
    for (const type of ['', 'unknown_type']) {
      const label = objectLabel('', type)
      expect(label).toBe('—')
      expect(label).not.toMatch(/[0-9a-f]{8}-[0-9a-f]{4}/)
    }
  })
})

describe('destructiveAction', () => {
  // These are the rows an operator must be able to spot in a wall of routine
  // entries: they destroy or expose evidence.
  test('flags the actions that destroy or expose evidence', () => {
    for (const action of [
      'recording.deleted_local',
      'user.disabled',
      'user.grants_changed',
      'storage.deleted',
      'auth.session_revoked',
    ]) {
      expect(destructiveAction(action)).toBe(true)
    }
  })

  test('does not flag routine reads and logins', () => {
    for (const action of ['recording.streamed', 'auth.login', 'channel.enabled']) {
      expect(destructiveAction(action)).toBe(false)
    }
  })
})

describe('groupAudits', () => {
  const row = (overrides: Partial<Parameters<typeof groupAudits>[0][number]>) => ({
    created_at: '2026-10-17T11:27:05.605020Z',
    action: 'recording.deleted_local',
    actor_id: null,
    object_type: 'recording_segment',
    object_label: '2026-10-11 04:49 的录像',
    ...overrides,
  })

  // A sweep writes one row per segment. Showing 1246 identical lines makes the
  // trail unreadable, so they collapse into one line that keeps the count.
  test('collapses a batch into one row that still shows its size', () => {
    const items = [
      row({}),
      row({}),
      row({}),
    ]
    const groups = groupAudits(items)
    expect(groups).toHaveLength(1)
    expect(groups[0].count).toBe(3)
    expect(groups[0].first.action).toBe('recording.deleted_local')
  })

  // Different actors are different events even at the same instant; merging them
  // would attribute one person's action to another.
  test('keeps different actors apart', () => {
    const groups = groupAudits([
      row({ actor_id: 'aaaa' }),
      row({ actor_id: 'bbbb' }),
    ])
    expect(groups).toHaveLength(2)
  })

  test('keeps different actions apart', () => {
    const groups = groupAudits([
      row({}),
      row({ action: 'recording.streamed' }),
    ])
    expect(groups).toHaveLength(2)
  })

  test('preserves input order', () => {
    const groups = groupAudits([
      row({ action: 'auth.login', object_type: 'user' }),
      row({}),
    ])
    expect(groups.map((g) => g.first.action)).toEqual([
      'auth.login',
      'recording.deleted_local',
    ])
  })
})
