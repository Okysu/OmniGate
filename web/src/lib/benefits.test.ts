import { describe, expect, it } from 'vitest'
import { campaignState, emptyCampaign, type PublicCampaign } from './benefits'
import { buildPlanPayload, emptyPlanForm, formFromPlan, validatePlanForm } from './planForm'
import type { Plan } from './types'

describe('福利活动状态', () => {
  const c: PublicCampaign = { ...emptyCampaign(), requirements: [] }
  const now = Date.parse('2026-11-11T00:00:00Z')
  it('时间边界、售罄和不限量', () => {
    expect(campaignState(c, now)).toBe('进行中')
    expect(campaignState({ ...c, startsAt: new Date(now + 1).toISOString() }, now)).toBe('未开始')
    expect(campaignState({ ...c, startsAt: new Date(now).toISOString() }, now)).toBe('进行中')
    expect(campaignState({ ...c, endsAt: new Date(now).toISOString() }, now)).toBe('已结束')
    expect(campaignState({ ...c, stock: 0 }, now)).toBe('已领完')
  })
  it('库存默认不限量，活动每人一次', () => {
    expect(emptyCampaign()).toMatchObject({ stock: -1, perUserLimit: 1, status: 'draft' })
  })
})

describe('套餐库存表单', () => {
  it('保留售罄和隐藏标记', () => {
    const f = emptyPlanForm()
    f.stock = 0
    f.hidden = true
    const payload = buildPlanPayload(f)
    expect(payload).toMatchObject({ stock: 0, hidden: true })
    expect(formFromPlan({ ...payload, id: 'p' } as Plan)).toMatchObject({ stock: 0, hidden: true })
  })
  it.each([-2, 0.5, '', 'a'])('拒绝非法库存 %s', (stock) => {
    const f = emptyPlanForm()
    f.stock = stock
    expect(validatePlanForm(f)).toHaveProperty('stock')
  })
})
