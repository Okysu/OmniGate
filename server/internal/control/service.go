package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/netguard"
	"omnigate/internal/platform/secretbox"
)

type Settings struct {
	Config
	Version   int  `json:"version"`
	APIKeySet bool `json:"apiKeySet"`
}
type SaveInput struct {
	Config
	Version int     `json:"version"`
	APIKey  *string `json:"apiKey,omitempty"`
}
type snapshot struct {
	Settings
	Secret  string
	ActorID uuid.UUID
}
type Input struct {
	RequestID                string
	UserID, KeyID, ChannelID uuid.UUID
	Model, Dialect           string
	Body                     []byte
}
type Event struct {
	Threshold         float64    `json:"threshold"`
	ReviewProbability *float64   `json:"reviewProbability"`
	ReviewThreshold   *float64   `json:"reviewThreshold"`
	ReviewedAt        *time.Time `json:"reviewedAt"`
	SnapshotID        *uuid.UUID `json:"snapshotId"`
	ID                uuid.UUID  `json:"id"`
	RequestID         string     `json:"requestId"`
	RuleID            string     `json:"ruleId"`
	UserID            uuid.UUID  `json:"userId"`
	KeyID             uuid.UUID  `json:"keyId"`
	ChannelID         uuid.UUID  `json:"channelId"`
	Model             string     `json:"model"`
	Dialect           string     `json:"dialect"`
	Reason            string     `json:"reason"`
	Probability       *float64   `json:"probability"`
	Scores            []Score    `json:"scores"`
	Excerpt           string     `json:"excerpt"`
	Failure           bool       `json:"failure"`
	Action            string     `json:"action"`
	ActionError       string     `json:"actionError"`
	ActionState       string     `json:"actionState"`
	CreatedAt         time.Time  `json:"createdAt"`
}
type Service struct {
	ReviewModels func() []string
	OnReview     func(context.Context, string, string) (string, error)
	reviewSlots  chan struct{}
	pool         *db.DB
	box          *secretbox.Keyring
	audit        *audit.Recorder
	client       *http.Client
	mu           sync.Mutex
	cached       snapshot
	expires      time.Time
	// OnHit applies native platform actions after a committed, deduplicated hit.
	// Classifier failures never invoke it. Rule editors are attributed in audit.
	OnHit func(context.Context, uuid.UUID, Rule, Event) error
}

func New(pool *db.DB, box *secretbox.Keyring, rec *audit.Recorder, transport http.RoundTripper) *Service {
	if transport == nil {
		transport = netguard.NewTransport(netguard.Options{})
	}
	return &Service{pool: pool, box: box, audit: rec, client: &http.Client{Transport: transport, CheckRedirect: netguard.NoRedirect}, reviewSlots: make(chan struct{}, 4)}
}
func (s *Service) load(ctx context.Context) (snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.expires) {
		return s.cached, nil
	}
	p := snapshot{Settings: Settings{Config: DefaultConfig()}}
	var raw string
	var actor *uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT config,secret,version,updated_by FROM control_settings WHERE id=1`).Scan(&raw, &p.Secret, &p.Version, &actor)
	if err != nil && !db.IsNoRows(err) {
		return p, err
	}
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &p.Config); err != nil {
			return p, err
		}
		for i := range p.Rules {
			if err := p.Rules[i].compileModels(); err != nil {
				return p, err
			}
		}
		if actor != nil {
			p.ActorID = *actor
		}
	}
	p.APIKeySet = p.Secret != ""
	s.cached = p
	s.expires = time.Now().Add(5 * time.Second)
	return p, nil
}
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	p, err := s.load(ctx)
	return p.Settings, err
}

func (s *Service) validate(ctx context.Context, c Config) error {
	bad := func(msg string) error { return apperr.Validation(msg, nil) }
	if c.ExcerptChars < 180 || c.ExcerptChars > 10000 || c.SnapshotWindowSeconds < 60 || c.SnapshotWindowSeconds > 86400 {
		return bad("摘要长度需为 180—10000，快照去重窗口需为 60—86400 秒")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return bad("Jev Base URL 必须是无凭据、查询参数的 HTTP(S) API 根地址")
	}
	if strings.TrimSpace(c.Model) == "" || len(c.Model) > 200 || math.IsNaN(c.Threshold) || c.Threshold < 0 || c.Threshold > 1 || c.TimeoutMs < 1000 || c.TimeoutMs > 30000 || c.MinChars < 0 || c.MinChars > 500 || c.Keep < 1 || c.Keep > 10000 || len(c.Categories) > 32 || len(c.Rules) > 100 {
		return bad("请检查模型、阈值、分类、超时、最短文本及保留条数")
	}
	for _, line := range c.Categories {
		if len([]rune(line)) > 4000 {
			return bad("分类描述不能超过 4000 字")
		}
	}
	seen := map[string]bool{}
	for _, r := range c.Rules {
		id, err := uuid.Parse(r.ID)
		if err != nil || id == uuid.Nil || seen[r.ID] || strings.TrimSpace(r.Name) == "" || len([]rune(r.Name)) > 100 || !slices.Contains([]string{"audit", "block", "notify", "ban"}, r.Action) || len(r.ChannelIDs) > 200 || len(r.Models) > 200 {
			return bad("管控规则的 ID、名称、动作或匹配范围无效")
		}
		seen[r.ID] = true
		for _, m := range r.Models {
			if m == "" || len(m) > 200 {
				return bad("模型匹配表达式无效")
			}
		}
		if err := r.compileModels(); err != nil {
			return bad("模型通配符或正则表达式无效：" + err.Error())
		}
		for _, ch := range r.ChannelIDs {
			id, err := uuid.Parse(ch)
			if err != nil || id == uuid.Nil {
				return bad("渠道 ID 无效")
			}
			var n int
			if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM channels WHERE id=$1`, id).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return bad("指定渠道不存在")
			}
		}
	}
	return nil
}

func (s *Service) Save(ctx context.Context, p *authz.Principal, in SaveInput) (Settings, error) {
	if !p.Can(authz.SettingsWrite) || !p.Can(authz.UsersWrite) {
		return Settings{}, apperr.Forbidden()
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if err := s.validate(ctx, in.Config); err != nil {
		return Settings{}, err
	}
	raw, _ := json.Marshal(in.Config)
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var version int
		var secret string
		err := tx.QueryRow(ctx, `SELECT version,secret FROM control_settings WHERE id=1 FOR UPDATE`).Scan(&version, &secret)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		if version != in.Version {
			return apperr.VersionConflict()
		}
		if in.APIKey != nil {
			key := strings.TrimSpace(*in.APIKey)
			if key == "" {
				secret = ""
			} else {
				if len(key) > 4096 {
					return apperr.Validation("Jev Key 过长", nil)
				}
				secret, err = s.box.Seal([]byte(key), "jev-key")
				if err != nil {
					return err
				}
			}
		}
		if in.Enabled && secret == "" {
			return apperr.Validation("启用管控前请填写 Jev API Key", nil)
		}
		if version == 0 {
			tag, insertErr := tx.Exec(ctx, `INSERT INTO control_settings(id,config,secret,version,updated_by,updated_at) VALUES(1,$1,$2,1,$3,$4) ON CONFLICT DO NOTHING`, string(raw), secret, p.UserID, time.Now().UTC())
			err = insertErr
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return apperr.VersionConflict()
			}
		} else {
			_, err = tx.Exec(ctx, `UPDATE control_settings SET config=$1,secret=$2,version=version+1,updated_by=$3,updated_at=$4 WHERE id=1`, string(raw), secret, p.UserID, time.Now().UTC())
			if err != nil {
				return err
			}
		}
		return s.audit.Record(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorName: &p.Name, Action: "control.settings_update", ResourceType: "control", Metadata: map[string]any{"enabled": in.Enabled, "rules": len(in.Rules), "version": version + 1, "keyChanged": in.APIKey != nil}})
	})
	if err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	s.expires = time.Time{}
	s.mu.Unlock()
	return s.Settings(ctx)
}

func (s *Service) classify(ctx context.Context, p snapshot, text string) ([]Score, string, *float64, error) {
	key, err := s.box.Open(p.Secret, "jev-key")
	if err != nil {
		return nil, "", nil, fmt.Errorf("key_unavailable")
	}
	cats := categories(p.Categories)
	questions := map[string]any{}
	for _, c := range cats {
		questions[c.ID] = map[string]any{"type": "noul", "instructions": c.Ask, "criteria": map[string]string{"true": c.Yes, "false": c.No}}
	}
	raw, _ := json.Marshal(map[string]any{"model": p.Model, "state": map[string]string{"text": text}, "questions": questions})
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutMs)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/systemone", bytes.NewReader(raw))
	if err != nil {
		return nil, "", nil, fmt.Errorf("invalid_endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+string(key))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", nil, fmt.Errorf("fetch_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", nil, fmt.Errorf("http_%d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, "", nil, fmt.Errorf("invalid_response")
	}
	return judgment(body, cats, p.Threshold)
}

// Check returns whether the request must stop. Checked is local to one gateway
// request: retries do not repeat classifier billing, notifications or sanctions.
func (s *Service) Check(ctx context.Context, in Input, checked map[string]bool) (bool, error) {
	p, err := s.load(ctx)
	if err != nil {
		return false, err
	}
	if !p.Enabled || len(categories(p.Categories)) == 0 {
		return false, nil
	}
	var rule *Rule
	for i := range p.Rules {
		if p.Rules[i].matches(in.ChannelID.String(), in.Model) {
			rule = &p.Rules[i]
			break
		}
	}
	if rule == nil || checked[rule.ID] {
		return false, nil
	}
	text := Extract(in.Body)
	if strings.TrimSpace(text) == "" || len([]rune(text)) < p.MinChars {
		return false, nil
	}
	scores, reason, prob, checkErr := s.classify(ctx, p, fit(text))
	checked[rule.ID] = true
	if checkErr == nil && prob == nil {
		return false, nil
	}
	e := Event{ID: uuid.Must(uuid.NewV7()), RequestID: in.RequestID, RuleID: rule.ID, UserID: in.UserID, KeyID: in.KeyID, ChannelID: in.ChannelID, Model: in.Model, Dialect: in.Dialect, Reason: reason, Probability: prob, Scores: scores, Failure: checkErr != nil, Action: rule.Action, CreatedAt: time.Now().UTC(), Threshold: p.Threshold}
	blocked := rule.Action == "block" || rule.Action == "ban"
	if checkErr != nil {
		e.Reason = checkErr.Error()
		e.Action = "audit"
		blocked = p.FailClosed
		if blocked {
			e.Action = "block"
		}
	}
	if p.StoreExcerpt {
		e.Excerpt = clip(strings.Join(strings.Fields(text), " "), p.ExcerptChars)
	}
	data, _ := json.Marshal(scores)
	// Persist before automation. A storage failure must never silently apply a
	// destructive action with no attributable evidence.
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	var inserted bool
	err = db.InTx(work, s.pool, func(tx db.Tx) error {
		if p.StoreSnapshots {
			snapshotID, err := saveSnapshot(work, tx, in, time.Duration(p.SnapshotWindowSeconds)*time.Second)
			if err != nil {
				return err
			}
			e.SnapshotID = &snapshotID
		}
		tag, err := tx.Exec(work, `INSERT INTO control_events(id,request_id,rule_id,user_id,key_id,channel_id,model,dialect,reason,probability,scores,excerpt,failure,action,created_at,snapshot_id,threshold)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) ON CONFLICT(request_id,rule_id) DO NOTHING`, e.ID, e.RequestID, e.RuleID, e.UserID, e.KeyID, e.ChannelID, e.Model, e.Dialect, e.Reason, e.Probability, string(data), e.Excerpt, e.Failure, e.Action, e.CreatedAt, e.SnapshotID, e.Threshold)
		inserted = err == nil && tag.RowsAffected() > 0
		return err
	})
	if err != nil {
		return true, err
	}
	if inserted {
		state := "completed"
		if !e.Failure && s.OnHit != nil {
			if actionErr := s.OnHit(work, p.ActorID, *rule, e); actionErr != nil {
				e.ActionError = clip(actionErr.Error(), 500)
				state = "failed"
			}
		}
		if _, saveErr := s.pool.Exec(work, `UPDATE control_events SET action_error=$2, action_state=$3 WHERE id=$1`, e.ID, e.ActionError, state); saveErr != nil {
			return true, saveErr
		}
	}
	err = db.InTx(work, s.pool, func(tx db.Tx) error {
		if _, err := tx.Exec(work, `DELETE FROM control_events WHERE id IN (SELECT id FROM control_events ORDER BY created_at DESC,id DESC LIMIT 100000000 OFFSET $1)`, p.Keep); err != nil {
			return err
		}
		return pruneSnapshots(work, tx)
	})
	return blocked, err
}
