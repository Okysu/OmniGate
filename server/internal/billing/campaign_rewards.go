package billing

import (
	"encoding/json"
	"github.com/google/uuid"
	"omnigate/internal/apperr"
	"omnigate/internal/money"
	"omnigate/internal/subscription"
)

func (c *Campaign) rewardItems() []CampaignRewardItem {
	if c.RewardType == "bundle" {
		return c.Rewards
	}
	r := CampaignRewardItem{Type: c.RewardType, PlanID: c.PlanID, Amount: c.Amount}
	if c.Reward != nil {
		r.Quantity = c.Reward.Quantity
		r.Kind = c.Reward.Kind
		r.ValidDays = c.Reward.ValidDays
		r.PlanIDs = c.Reward.PlanIDs
	}
	return []CampaignRewardItem{r}
}
func prepareRewards(c *Campaign) error {
	for _, r := range c.Rewards {
		if err := validateRewardItem(r); err != nil {
			return err
		}
	}
	if len(c.Rewards) > 10 {
		return apperr.Validation("每个活动最多配置 10 项奖励", nil)
	}
	if len(c.Rewards) == 1 {
		r := c.Rewards[0]
		c.RewardType = r.Type
		c.PlanID = r.PlanID
		c.Amount = r.Amount
		c.Reward = nil
		if r.Type == "reset_card" || r.Type == "lottery" {
			c.Reward = &CampaignReward{Quantity: r.Quantity, Kind: r.Kind, ValidDays: r.ValidDays, PlanIDs: r.PlanIDs}
		}
		c.Rewards = nil
	} else if len(c.Rewards) > 1 {
		c.RewardType = "bundle"
		c.PlanID = nil
		c.Amount = "0"
		c.Reward = nil
	}
	if c.RewardType == "bundle" && len(c.Rewards) < 2 {
		return apperr.Validation("组合活动须包含至少两项奖励", nil)
	}
	return nil
}
func validateRewardItem(r CampaignRewardItem) error {
	fail := func() error { return apperr.Validation("请检查奖励类型、金额、数量和套餐配置", nil) }
	amount := money.Amount(0)
	var err error
	if r.Amount != "" {
		amount, err = money.Parse(r.Amount)
		if err != nil || amount < 0 {
			return fail()
		}
	}
	switch r.Type {
	case "balance":
		if amount <= 0 || r.PlanID != nil || r.Quantity != 0 || r.Kind != "" || r.ValidDays != 0 || len(r.PlanIDs) > 0 {
			return fail()
		}
	case "subscription":
		if r.PlanID == nil || *r.PlanID == uuid.Nil || amount != 0 || r.Quantity != 0 || r.Kind != "" || r.ValidDays != 0 || len(r.PlanIDs) > 0 {
			return fail()
		}
	case "lottery", "reset_card":
		if r.Quantity < 1 || r.Quantity > 100 || amount != 0 || r.PlanID != nil {
			return fail()
		}
		if r.Type == "lottery" {
			if r.Kind != "" || r.ValidDays != 0 || len(r.PlanIDs) > 0 {
				return fail()
			}
		} else {
			if !subscription.ValidCardKind(r.Kind) || r.ValidDays < 0 || r.ValidDays > 366 || len(r.PlanIDs) > 50 {
				return fail()
			}
			for _, id := range r.PlanIDs {
				if id == uuid.Nil {
					return fail()
				}
			}
		}
	default:
		return fail()
	}
	return nil
}
func sameRewardItems(a, b []CampaignRewardItem) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
