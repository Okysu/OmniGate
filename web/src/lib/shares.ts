// Channel share acceptance (phase5-api.md §5): status labels, the owner's per-recipient
// view of the sharing form and the recipient's incoming list.
import type { ChannelShare, IncomingShare, SharedWith, ShareStatus } from './types'

export const SHARE_STATUS_LABELS: Record<ShareStatus, string> = {
  pending: '待接受',
  accepted: '已接受',
  declined: '已拒绝',
}

/** Badge colours (light / dark) per status. */
export const SHARE_STATUS_CLASSES: Record<ShareStatus, string> = {
  pending: 'border-amber-500/50 text-amber-700 dark:text-amber-400',
  accepted: 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400',
  declined: 'border-destructive/50 text-destructive',
}

export function isShareStatus(v: unknown): v is ShareStatus {
  return v === 'pending' || v === 'accepted' || v === 'declined'
}

/** Owner view: invited user id → share (older backends without `shares`: empty). */
export function shareIndex(shares: ChannelShare[] | null | undefined): Map<string, ChannelShare> {
  const out = new Map<string, ChannelShare>()
  for (const s of shares ?? []) {
    if (s && typeof s.userId === 'string' && isShareStatus(s.status))
      out.set(s.userId, s)
  }
  return out
}

/**
 * Status shown next to a selected user in the sharing form: the server status when the
 * user was already invited, `new` when added in this form (saving sends the invitation),
 * and `reinvite` for a declined user added back (saving sends a new invitation).
 */
export type SelectionStatus = ShareStatus | 'new' | 'reinvite'

export function selectionStatus(userId: string, index: Map<string, ChannelShare>): SelectionStatus {
  const s = index.get(userId)
  if (!s)
    return 'new'
  return s.status === 'declined' ? 'reinvite' : s.status
}

export const SELECTION_STATUS_LABELS: Record<SelectionStatus, string> = {
  ...SHARE_STATUS_LABELS,
  new: '保存后邀请',
  reinvite: '保存后重新邀请',
}

/** Declined (or left) recipients that are not selected again: shown with "重新邀请". */
export function declinedShares(shares: ChannelShare[] | null | undefined, selection: SharedWith): ChannelShare[] {
  const selected = new Set(selection.users)
  return [...shareIndex(shares).values()].filter(s => s.status === 'declined' && !selected.has(s.userId))
}

/** Adds a user back to the selection (saving re-invites a declined recipient). */
export function reinvite(selection: SharedWith, userId: string): SharedWith {
  if (selection.users.includes(userId))
    return selection
  return { ...selection, users: [...selection.users, userId] }
}

/** Groups in `next` that `original` did not have (only `channels.manage` may add groups, §5.2). */
export function addedGroups(original: string[], next: string[]): string[] {
  const had = new Set(original)
  return [...new Set(next)].filter(g => !had.has(g))
}

export const GROUP_SHARE_ADMIN_ONLY = '只有渠道管理员可以共享给用户组'

/** Recipient view: pending invitations and accepted shares, newest first within each. */
export function splitIncoming(items: IncomingShare[]): { pending: IncomingShare[], accepted: IncomingShare[] } {
  const newest = (a: IncomingShare, b: IncomingShare) => (b.createdAt ?? '').localeCompare(a.createdAt ?? '')
  return {
    pending: items.filter(i => i.status === 'pending').sort(newest),
    accepted: items.filter(i => i.status === 'accepted').sort(newest),
  }
}

/** Number of invitations waiting for an answer (tab badge). */
export function pendingCount(items: IncomingShare[] | null | undefined): number {
  return (items ?? []).filter(i => i.status === 'pending').length
}

/** Replaces (or drops, when `next` is null) the item of `channelId`. */
export function updateIncoming(items: IncomingShare[], channelId: string, next: IncomingShare | null): IncomingShare[] {
  if (!next)
    return items.filter(i => i.channelId !== channelId)
  return items.map(i => (i.channelId === channelId ? next : i))
}
