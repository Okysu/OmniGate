// Plan purchase page (phase15-api.md §2–§3): view models for the plan cards, the
// confirm dialog and error messages. Pure functions; money formatting is injected
// (`useCurrency().money`) and amounts stay decimal strings (BigInt nano arithmetic).
import type { MoneyFormatter } from './quota'
import type { PurchaseAction, PurchaseInput, PurchaseOption, PurchaseOptions, PurchaseResult } from './types'
import { errorMessage, isApiError } from './api'
import { formatDateTime } from './format'
import { amountSign, fromNano, isValidAmount, toNano } from './money'
import { PLAN_ERROR_MESSAGES } from './quota'

export const PURCHASE_ACTION_LABELS: Record<PurchaseAction, string> = {
  new: '购买',
  renew: '续费',
  upgrade: '升级',
}

/** One thing the user can buy: a direct purchase / renewal, or an upgrade from a subscription. */
export interface PurchaseIntent {
  action: PurchaseAction
  planId: string
  planName: string
  /** Price shown to the user (sent back as `expectedPrice`). */
  price: string
  /** Upgrade source (null for new / renew). */
  fromSubscriptionId: string | null
  fromPlanName: string | null
  /** Renew: the current end date. */
  currentEndsAt: string | null
  /** End date after the purchase (upgrade: a new term starting now). */
  endsAt: string | null
  /** Upgrade: value of the unused old time deducted from the price. */
  credit: string | null
}

export interface PurchaseCard {
  option: PurchaseOption
  /** Direct purchase / renewal; null when the plan has no price. */
  direct: PurchaseIntent | null
  upgrades: PurchaseIntent[]
  /** Show the direct buy / renew button: hidden when the plan can be reached by upgrading (only the upgrade is offered). */
  showDirect: boolean
}

function hasPrice(v: string | null | undefined): v is string {
  return typeof v === 'string' && isValidAmount(v) && amountSign(v) > 0
}

/** Direct purchase (action `new` / `renew`) of an option, or null when it is not for sale. */
export function directIntent(o: PurchaseOption): PurchaseIntent | null {
  if (!o.purchasable || !hasPrice(o.price))
    return null
  return {
    action: o.action === 'renew' ? 'renew' : 'new',
    planId: o.plan.id,
    planName: o.plan.name,
    price: o.price,
    fromSubscriptionId: null,
    fromPlanName: null,
    currentEndsAt: o.action === 'renew' ? o.currentEndsAt : null,
    endsAt: o.newEndsAt,
    credit: null,
  }
}

/** Upgrades into an option's plan, cheapest first (the server already sorts; kept stable). */
export function upgradeIntents(o: PurchaseOption): PurchaseIntent[] {
  return (o.upgrades ?? [])
    .filter(u => hasPrice(u.price))
    .map(u => ({
      action: 'upgrade' as const,
      planId: o.plan.id,
      planName: o.plan.name,
      price: u.price,
      fromSubscriptionId: u.fromSubscriptionId,
      fromPlanName: u.fromPlanName,
      currentEndsAt: null,
      endsAt: u.endsAt,
      credit: u.credit ?? null,
    }))
    .sort((a, b) => compareAmounts(a.price, b.price))
}

export function buildPurchaseCards(opts: PurchaseOptions | null | undefined): PurchaseCard[] {
  return (opts?.plans ?? []).map((option) => {
    const direct = directIntent(option)
    const upgrades = upgradeIntents(option)
    return { option, direct, upgrades, showDirect: direct !== null && upgrades.length === 0 }
  })
}

/** Finds the same intent (same plan and upgrade source) in freshly loaded options. */
export function findIntent(opts: PurchaseOptions | null | undefined, planId: string, fromSubscriptionId: string | null): PurchaseIntent | null {
  const option = opts?.plans.find(p => p.plan.id === planId)
  if (!option)
    return null
  if (fromSubscriptionId === null)
    return directIntent(option)
  return upgradeIntents(option).find(i => i.fromSubscriptionId === fromSubscriptionId) ?? null
}

/** Button text: "购买" / "续费" / "从 Go 升级 · 补差价 $3.000". */
export function intentButtonLabel(i: PurchaseIntent, money: MoneyFormatter): string {
  if (i.action === 'upgrade')
    return `从 ${i.fromPlanName ?? '当前套餐'} 升级 · 补差价 ${money(i.price)}`
  return PURCHASE_ACTION_LABELS[i.action]
}

/** Confirm dialog title: "购买套餐「Pro」" / "续费套餐「Pro」" / "升级套餐：Go → Pro". */
export function intentTitle(i: PurchaseIntent): string {
  if (i.action === 'upgrade')
    return `升级套餐：${i.fromPlanName ?? '当前套餐'} → ${i.planName}`
  return `${PURCHASE_ACTION_LABELS[i.action]}套餐「${i.planName}」`
}

function compareAmounts(a: string, b: string): number {
  const d = toNano(a) - toNano(b)
  return d < 0n ? -1 : d > 0n ? 1 : 0
}

/** `available − price` as a decimal string; null when either is not a valid amount. */
export function balanceAfter(available: string | null | undefined, price: string | null | undefined): string | null {
  if (available == null || price == null || !isValidAmount(available, { allowNegative: true }) || !isValidAmount(price))
    return null
  return fromNano(toNano(available) - toNano(price))
}

/** True when the available balance covers the price (contract: `available ≥ price`). */
export function canAfford(available: string | null | undefined, price: string | null | undefined): boolean {
  const after = balanceAfter(available, price)
  return after !== null && amountSign(after) >= 0
}

/** Amount still missing to afford `price` (null when affordable or invalid). */
export function shortfall(available: string | null | undefined, price: string | null | undefined): string | null {
  const after = balanceAfter(available, price)
  if (after === null || amountSign(after) >= 0)
    return null
  return after.replace(/^-/, '')
}

export function purchaseBody(i: PurchaseIntent): PurchaseInput {
  const body: PurchaseInput = { planId: i.planId, expectedPrice: i.price }
  if (i.fromSubscriptionId)
    body.fromSubscriptionId = i.fromSubscriptionId
  return body
}

/** Current price from a `409 price_changed` error (`details.price`), else null. */
export function changedPrice(err: unknown): string | null {
  if (!isApiError(err) || err.code !== 'price_changed')
    return null
  const p = err.details.price
  return typeof p === 'string' && isValidAmount(p) ? p : null
}

/** Friendly message for a failed `POST /api/billing/purchase`. */
export function purchaseErrorMessage(err: unknown, money: MoneyFormatter): string {
  if (!isApiError(err))
    return errorMessage(err)
  switch (err.code) {
    case 'insufficient_balance': {
      const { price, available } = err.details
      if (price && available)
        return `钱包余额不足：需要 ${money(price)}，当前可用 ${money(available)}。请先使用兑换码充值。`
      return PLAN_ERROR_MESSAGES.insufficient_balance!
    }
    case 'price_changed': {
      const p = changedPrice(err)
      return p ? `套餐价格已变为 ${money(p)}，请确认新价格后重试。` : PLAN_ERROR_MESSAGES.price_changed!
    }
    case 'not_found':
      return '套餐或升级来源订阅不存在（可能已被删除或不属于你），请刷新后重试。'
    default:
      return PLAN_ERROR_MESSAGES[err.code] ?? errorMessage(err)
  }
}

/** Success toast: title and description. */
export function purchaseSuccessText(r: PurchaseResult, money: MoneyFormatter): { title: string, description: string } {
  const name = r.subscription.plan.name
  const until = formatDateTime(r.subscription.endsAt)
  const paid = `已扣款 ${money(r.price)}，可用余额 ${money(r.wallet.available)}`
  switch (r.action) {
    case 'renew':
      return { title: '续费成功', description: `套餐「${name}」已续期至 ${until}；${paid}。` }
    case 'upgrade':
      return { title: '升级成功', description: `已升级为「${name}」，新周期从现在起至 ${until}；${paid}。` }
    default:
      return { title: '购买成功', description: `套餐「${name}」已开通，有效期至 ${until}；${paid}。` }
  }
}

/**
 * Plan descriptions (often several lines of rules) are shown in full; only very long
 * ones collapse to a few lines behind a 展开 / 收起 toggle.
 */
export function isLongDescription(d: string): boolean {
  return d.length > 400 || d.split('\n').length > 10
}
