package billing

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"math/big"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/lottery"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
)

type LotteryPrize struct {
	Amount  string `json:"amount"`
	Percent int    `json:"percent"`
}

type LotteryConfig struct {
	Threshold string         `json:"threshold"`
	Prizes    []LotteryPrize `json:"prizes"`
	Version   int            `json:"version"`
}

func lotteryConfig(ctx context.Context, q db.Querier) (LotteryConfig, error) {
	var c LotteryConfig
	var threshold int64
	var raw string
	err := q.QueryRow(ctx, "SELECT threshold_nano,prizes,version FROM lottery_config WHERE id=1").Scan(&threshold, &raw, &c.Version)
	if err != nil {
		return c, err
	}
	c.Threshold = money.Amount(threshold).String()
	err = json.Unmarshal([]byte(raw), &c.Prizes)
	return c, err
}

func (s *Service) SetLottery(ctx context.Context, a Actor, c LotteryConfig, m RequestMeta) (LotteryConfig, error) {
	threshold, err := money.Parse(c.Threshold)
	if err != nil || threshold < 0 {
		return c, apperr.Validation("消费门槛必须是非负金额", nil)
	}
	if len(c.Prizes) == 0 || len(c.Prizes) > 100 {
		return c, apperr.Validation("请配置 1 到 100 个奖项", nil)
	}
	sum := 0
	for i, p := range c.Prizes {
		amount, err := money.Parse(p.Amount)
		if err != nil || amount <= 0 || amount > 3_000_000_000 || amount%10_000_000 != 0 || p.Percent < 1 || p.Percent > 100 {
			return c, apperr.Validation("单次奖金须为 0.01 到 3.00，概率须为 1 到 100 的整数", nil)
		}
		c.Prizes[i].Amount = amount.String()
		sum += p.Percent
	}
	if sum != 100 {
		return c, apperr.Validation("中奖概率合计必须为 100%", nil)
	}
	raw, err := json.Marshal(c.Prizes)
	if err != nil {
		return c, err
	}
	c.Threshold = threshold.String()
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		res, err := tx.Exec(ctx, `UPDATE lottery_config SET threshold_nano=$1,prizes=$2,version=version+1 WHERE id=1 AND version=$3`, int64(threshold), string(raw), c.Version)
		if err != nil {
			return err
		}
		if res.RowsAffected() != 1 {
			return apperr.VersionConflict()
		}
		grants, err := tx.Exec(ctx, "UPDATE lottery_days SET earned=true WHERE NOT earned AND spent_nano>$1", int64(threshold))
		if err != nil {
			return err
		}
		c.Version++
		return s.audit(ctx, tx, a, m, "lottery.configure", "lottery", "1", map[string]any{"config": c, "historicalChancesGranted": grants.RowsAffected()})
	})
	return c, err
}

type LotteryStatus struct {
	LotteryConfig
	Available   int    `json:"available"`
	Earned      int    `json:"earned"`
	TodaySpent  string `json:"todaySpent"`
	TodayEarned bool   `json:"todayEarned"`
}

const chanceQuery = `SELECT (SELECT count(*) FROM lottery_days WHERE user_id=$1 AND earned) -
 (SELECT COALESCE(sum(draw_count),0) FROM lottery_draws WHERE user_id=$1)`

func (s *Service) Lottery(ctx context.Context, user uuid.UUID) (LotteryStatus, error) {
	var status LotteryStatus
	c, err := lotteryConfig(ctx, s.pool)
	if err != nil {
		return status, err
	}
	status.LotteryConfig = c
	today := lottery.Day(s.now())
	var spend int64
	err = s.pool.QueryRow(ctx, chanceQuery+`,(SELECT count(*) FROM lottery_days WHERE user_id=$1 AND earned),
 (SELECT COALESCE(sum(spent_nano),0) FROM lottery_days WHERE user_id=$1 AND day=$2),
 EXISTS(SELECT 1 FROM lottery_days WHERE user_id=$1 AND day=$2 AND earned)`, user, today).Scan(&status.Available, &status.Earned, &spend, &status.TodayEarned)
	status.TodaySpent = money.Amount(spend).String()
	return status, err
}

type LotteryResult struct {
	Rewards  []string  `json:"rewards"`
	Total    string    `json:"total"`
	LedgerID uuid.UUID `json:"ledgerId"`
}

func prizeAt(prizes []LotteryPrize, roll int) (money.Amount, error) {
	for _, p := range prizes {
		if roll < p.Percent {
			return money.Parse(p.Amount)
		}
		roll -= p.Percent
	}
	return 0, apperr.Validation("抽奖概率配置不完整", nil)
}

func (s *Service) DrawLottery(ctx context.Context, user, key uuid.UUID, count int) (LotteryResult, error) {
	var out LotteryResult
	if key == uuid.Nil || (count != 1 && count != 5 && count != 10 && count != 20) {
		return out, apperr.Validation("请选择单抽、5 连抽、10 连抽或 20 连抽，并提供请求 ID", nil)
	}
	var wallet *walletRow
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var err error
		wallet, err = s.lockWallet(ctx, tx, user, true)
		if err != nil {
			return err
		}
		var raw string
		var total money.Amount
		var savedCount int
		err = tx.QueryRow(ctx, "SELECT draw_count,rewards,total_nano,ledger_id FROM lottery_draws WHERE user_id=$1 AND request_id=$2", user, key).Scan(&savedCount, &raw, &total, &out.LedgerID)
		if err == nil {
			if savedCount != count {
				return apperr.Validation("相同请求 ID 已用于其他抽数", nil)
			}
			out.Total = total.String()
			return json.Unmarshal([]byte(raw), &out.Rewards)
		}
		if !db.IsNoRows(err) {
			return err
		}
		var available int
		if err = tx.QueryRow(ctx, chanceQuery, user).Scan(&available); err != nil {
			return err
		}
		if available < count {
			return apperr.Validation("可用抽奖次数不足", nil)
		}
		c, err := lotteryConfig(ctx, tx)
		if err != nil {
			return err
		}
		for range count {
			n, err := rand.Int(rand.Reader, big.NewInt(100))
			if err != nil {
				return err
			}
			amount, err := prizeAt(c.Prizes, int(n.Int64()))
			if err != nil {
				return err
			}
			total += amount
			out.Rewards = append(out.Rewards, amount.String())
		}
		note := "每日消费抽奖"
		entry, err := s.applyLedger(ctx, tx, wallet, KindGrant, total, "lottery", key.String(), &note, nil)
		if err != nil {
			return err
		}
		out.LedgerID = entry.ID
		out.Total = total.String()
		prizes, err := json.Marshal(out.Rewards)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO lottery_draws(user_id,request_id,draw_count,rewards,total_nano,ledger_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, user, key, count, string(prizes), int64(total), entry.ID, s.now())
		return err
	})
	if err == nil {
		s.dispatch(ctx, wallet)
	}
	return out, err
}
