import { describe, expect, it } from 'vitest'
import { emptyAudience, normalizeAudience } from './audience'
import { buildTarget } from './subscriptionBulk'
import { buildCardTarget, emptyIssueForm } from './resetCards'
import { campaignRewards, emptyCampaign, emptyReward, rewardTitle } from './benefits'
describe('shared audience and benefit reward adapters', () => {
  it('keeps explicit exclusions in bulk and card targets', () => {
    const filter = { ...emptyAudience(), excludePlanIds: ['gas'], quotaKind: 'weekly' as const }
    expect(buildTarget('plan', [], ['formal'], ['group'], filter)).toEqual({ filter: { ...filter, planIds: ['formal'], groupIds: ['group'], requireActiveSubscription: true } })
    expect(buildCardTarget({ ...emptyIssueForm(), targetType: 'filter', audience: filter })).toEqual({ type: 'filter', filter })
  })
  it('normalizes old and nullable condition lists without enabling filters', () => {
    expect(normalizeAudience({ groupIds: null as unknown as string[] })).toEqual(emptyAudience())
    expect(emptyAudience().excludePlanIds).toEqual([])
    expect(emptyAudience().requireActiveSubscription).toBe(false)
  })
  it('adapts old single rewards and preserves the whole combination', () => {
    const old = { ...emptyCampaign(), rewards: null, rewardType: 'lottery' as const, reward: { quantity: 5 } }
    expect(campaignRewards(old)).toEqual([{ type: 'lottery', quantity: 5 }])
    const items = [emptyReward('balance'), emptyReward('reset_card'), emptyReward('lottery')]
    expect(campaignRewards({ ...emptyCampaign(), rewards: items })).toEqual(items)
    expect(items.map(rewardTitle)).toEqual(['$1 余额', '1 张周重置卡', '1 次抽奖'])
  })
})
