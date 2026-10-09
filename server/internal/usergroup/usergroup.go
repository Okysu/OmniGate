// Package usergroup implements user groups (docs/contracts/phase8-api.md §1):
// every user belongs to exactly one group; a group carries the sell-price
// multiplier of platform channels, per-user request and spend limits and the
// timezone of their day / month windows. Exactly one group is the default
// (new users join it); deleting a group moves its members to the default.
package usergroup

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/money"
	"omnigate/internal/platform/tzcache"
	"omnigate/internal/pricing"
)

// Audit actions (§1.3).
const (
	ActionCreate      = "group.create"
	ActionUpdate      = "group.update"
	ActionDelete      = "group.delete"
	ActionUserChange  = "user.group_change"
	CodeGroupDefault  = "group_is_default"
	CodeNameExists    = "group_name_exists"
	maxNameLen        = 50
	maxDescriptionLen = 200
	maxRPM            = 1_000_000
	maxRPD            = 100_000_000
)

// Limits are per-user limits of a group's members (nil = unlimited).
type Limits struct {
	RPM          *int
	RPD          *int
	DailySpend   *money.Amount
	MonthlySpend *money.Amount
}

// Group is a user group.
type Group struct {
	ID          uuid.UUID
	Name        string
	Description string
	Multiplier  pricing.Multiplier
	Limits      Limits
	Timezone    string
	IsDefault   bool
	Members     int
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Location is the group's timezone (Asia/Shanghai when unreadable).
func (g *Group) Location() *time.Location { return tzcache.MustLoad(g.Timezone) }

// LimitsJSON is the API form of Limits (amounts as decimal strings).
type LimitsJSON struct {
	RPM          *int    `json:"rpm"`
	RPD          *int    `json:"rpd"`
	DailySpend   *string `json:"dailySpend"`
	MonthlySpend *string `json:"monthlySpend"`
}

func amountPtr(a *money.Amount) *string {
	if a == nil {
		return nil
	}
	s := a.String()
	return &s
}

// JSON renders the limits.
func (l Limits) JSON() LimitsJSON {
	return LimitsJSON{RPM: l.RPM, RPD: l.RPD, DailySpend: amountPtr(l.DailySpend), MonthlySpend: amountPtr(l.MonthlySpend)}
}

type groupJSON struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	PriceMultiplier string     `json:"priceMultiplier"`
	Limits          LimitsJSON `json:"limits"`
	Timezone        string     `json:"timezone"`
	IsDefault       bool       `json:"isDefault"`
	Members         int        `json:"members"`
	Version         int        `json:"version"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// MarshalJSON renders the UserGroup document of the contract.
func (g *Group) MarshalJSON() ([]byte, error) {
	return json.Marshal(groupJSON{ID: g.ID, Name: g.Name, Description: g.Description, PriceMultiplier: g.Multiplier.String(),
		Limits: g.Limits.JSON(), Timezone: g.Timezone, IsDefault: g.IsDefault, Members: g.Members, Version: g.Version,
		CreatedAt: g.CreatedAt.UTC(), UpdatedAt: g.UpdatedAt.UTC()})
}

// Ref names a group ({id, name}).
type Ref struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Self is a user's own group as /api/me shows it.
type Self struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	PriceMultiplier string     `json:"priceMultiplier"`
	Limits          LimitsJSON `json:"limits"`
	Timezone        string     `json:"timezone"`
}

// Self returns the /api/me view of g.
func (g *Group) Self() Self {
	return Self{ID: g.ID, Name: g.Name, PriceMultiplier: g.Multiplier.String(), Limits: g.Limits.JSON(), Timezone: g.Timezone}
}

// Decimal is a JSON decimal input accepted as a string or a number; null and
// "" are empty.
type Decimal struct {
	Set   bool   // the value is non-empty
	Value string // canonical text when Set
}

// UnmarshalJSON accepts "0.8", 0.8, null and "".
func (d *Decimal) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*d = Decimal{}
	if string(b) == "null" {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s = strings.TrimSpace(s); s != "" {
			d.Set, d.Value = true, s
		}
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	if f, err := strconv.ParseFloat(n.String(), 64); err == nil {
		// Numbers such as 1e-3 are rendered as plain decimals.
		d.Set, d.Value = true, strconv.FormatFloat(f, 'f', -1, 64)
	}
	return nil
}

// LimitsInput is the writable form of the limits; it replaces all four.
type LimitsInput struct {
	RPM          *int    `json:"rpm"`
	RPD          *int    `json:"rpd"`
	DailySpend   Decimal `json:"dailySpend"`
	MonthlySpend Decimal `json:"monthlySpend"`
}

// Input is the body of POST / PATCH /api/admin/groups.
type Input struct {
	Name            *string      `json:"name"`
	Description     *string      `json:"description"`
	PriceMultiplier *Decimal     `json:"priceMultiplier"`
	Limits          *LimitsInput `json:"limits"`
	Timezone        *string      `json:"timezone"`
	IsDefault       *bool        `json:"isDefault"`
	Version         *int         `json:"version"`
	// Read-only fields a client may send back unchanged (ignored).
	ID        any `json:"id"`
	Members   any `json:"members"`
	CreatedAt any `json:"createdAt"`
	UpdatedAt any `json:"updatedAt"`
}

// apply validates in onto g. create requires a name.
func (in *Input) apply(g *Group, create bool, details map[string]any) {
	if create && in.Name == nil {
		details["name"] = "必填"
	}
	if !create && in.Version == nil {
		details["version"] = "必填"
	}
	if in.Name != nil {
		g.Name = strings.TrimSpace(*in.Name)
		if n := len([]rune(g.Name)); n < 1 || n > maxNameLen {
			details["name"] = "长度应为 1–50 个字符"
		}
	}
	if in.Description != nil {
		g.Description = strings.TrimSpace(*in.Description)
		if len([]rune(g.Description)) > maxDescriptionLen {
			details["description"] = "不能超过 200 个字符"
		}
	}
	if in.PriceMultiplier != nil {
		if !in.PriceMultiplier.Set {
			details["priceMultiplier"] = "必填（0–100 的十进制数）"
		} else if m, err := pricing.ParseMultiplier(in.PriceMultiplier.Value, pricing.MaxGroupMultiplier); err != nil {
			details["priceMultiplier"] = err.Error()
		} else {
			g.Multiplier = m
		}
	}
	if in.Limits != nil {
		l := Limits{RPM: in.Limits.RPM, RPD: in.Limits.RPD}
		if l.RPM != nil && (*l.RPM < 1 || *l.RPM > maxRPM) {
			details["limits.rpm"] = "范围为 1–1000000，留空表示不限"
		}
		if l.RPD != nil && (*l.RPD < 1 || *l.RPD > maxRPD) {
			details["limits.rpd"] = "范围为 1–100000000，留空表示不限"
		}
		spend := func(d Decimal, field string) *money.Amount {
			if !d.Set {
				return nil
			}
			a, err := money.Parse(d.Value)
			if err != nil || a < 0 {
				details[field] = "必须是不小于 0 的十进制金额（最多 9 位小数），留空表示不限"
				return nil
			}
			return &a
		}
		l.DailySpend = spend(in.Limits.DailySpend, "limits.dailySpend")
		l.MonthlySpend = spend(in.Limits.MonthlySpend, "limits.monthlySpend")
		g.Limits = l
	}
	if in.Timezone != nil {
		g.Timezone = strings.TrimSpace(*in.Timezone)
		if g.Timezone == "" {
			g.Timezone = tzcache.Default
		}
		if !tzcache.Valid(g.Timezone) {
			details["timezone"] = "无效的时区（IANA 名称，如 Asia/Shanghai）"
		}
	}
}

// auditView is the audited state of a group.
func (g *Group) auditView() map[string]any {
	return map[string]any{"name": g.Name, "description": g.Description, "priceMultiplier": g.Multiplier.String(),
		"limits": g.Limits.JSON(), "timezone": g.Timezone, "isDefault": g.IsDefault}
}

// Change is a committed move of a user to another group.
type Change struct {
	UserID uuid.UUID
	From   Ref
	To     *Group
	// Deleted is set when the old group was deleted (members moved to the default).
	Deleted bool
	At      time.Time
}
