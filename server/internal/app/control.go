package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/adminapi"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/config"
	"omnigate/internal/control"
	"omnigate/internal/identity"
	"omnigate/internal/notify"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/netguard"
	"omnigate/internal/platform/secretbox"
)

func newControl(pool *db.DB, keys []secretbox.Key, rec *audit.Recorder, admin *adminapi.Handler, n *notify.Service, cfg *config.Config) (*control.Service, error) {
	box, err := secretbox.New("control-jev", keys)
	if err != nil {
		return nil, err
	}
	s := control.New(pool, box, rec, netguard.NewTransport(netguard.Options{AllowPrivate: cfg.ChannelsAllowPrivateNetwork}))
	s.OnHit = func(ctx context.Context, actorID uuid.UUID, r control.Rule, e control.Event) error {
		p := &authz.Principal{UserID: actorID, Name: "管控系统", Role: identity.RoleSystemAdmin}
		loc := time.FixedZone("北京时间", 8*3600)
		reason := fmt.Sprintf("自动管控规则「%s」命中 %s；请求 ID：%s。可联系管理员申请复核。", r.Name, e.Reason, e.RequestID)
		if len([]rune(reason)) > 200 {
			reason = string([]rune(reason)[:200])
		}
		var actionErr error
		if r.Action == "ban" {
			actionErr = admin.SuspendForControl(ctx, p, e.UserID, reason, e.RequestID)
		}
		body := fmt.Sprintf("您好，平台于 %s 检测到您的请求触发管控规则「%s」。\n模型：%s\n分类：%s\n请求 ID：%s\n本次处理：%s。自动分类可能存在误判，如有异议请联系管理员申请复核。", e.CreatedAt.In(loc).Format("2006-01-02 15:04:05"), r.Name, e.Model, e.Reason, e.RequestID, map[string]string{"audit": "记录审计", "block": "拦截请求", "notify": "发送提醒", "ban": "停用账号"}[r.Action])
		if actionErr != nil {
			body = "自动停用未完成，需管理员处理。\n" + body
		}
		if r.NotifyUser || r.Action == "notify" {
			_, err := n.Emit(ctx, notify.Event{Type: notify.TypeAdminMessage, Key: "control:" + e.ID.String() + ":user", Users: []uuid.UUID{e.UserID}, Title: "平台管控处理通知", Body: body, Severity: notify.SeverityWarn, Data: map[string]any{"forceEmail": true, "controlEventId": e.ID.String()}})
			actionErr = errors.Join(actionErr, err)
		}
		if r.NotifyAdmins {
			rows, err := pool.Query(ctx, `SELECT id FROM users WHERE role='system_admin' AND status='active'`)
			if err != nil {
				return err
			}
			ids, err := db.CollectRows[uuid.UUID](rows)
			if err != nil {
				return err
			}
			if len(ids) > 0 {
				_, err = n.Emit(ctx, notify.Event{Type: notify.TypeAdminMessage, Key: "control:" + e.ID.String() + ":admins", Users: ids, Title: "管控命中提醒", Body: fmt.Sprintf("用户 ID：%s\nKey ID：%s\n渠道 ID：%s\n%s", e.UserID, e.KeyID, e.ChannelID, body), Severity: notify.SeverityWarn, Link: "/console/admin/control", Data: map[string]any{"forceEmail": true, "controlEventId": e.ID.String()}})
				actionErr = errors.Join(actionErr, err)
			}
		}
		return actionErr
	}
	return s, nil
}
