package notify

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/identity"
)

func TestForcedMailSurvivesSuspensionAndRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("sender", identity.RoleSystemAdmin)
	u := e.user("recipient", identity.RoleUser)
	p := e.principal(admin, identity.RoleSystemAdmin)
	e.putPrefs(u, identity.RoleUser, `{"version":0,"email":{"enabled":false},"digest":"daily"}`)
	in := SendMessageInput{Target: MessageTarget{UserIDs: []uuid.UUID{u}}, Title: "停用通知", Body: "请联系管理员申诉", ForceEmail: true, IdempotencyKey: uuid.New()}
	sent, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{})
	if err != nil || sent.EventID == nil {
		t.Fatalf("send=%+v %v", sent, err)
	}
	query := EmailDeliveryQuery{EventID: sent.EventID, UserID: &u, Limit: 20}
	check := func(status string, attempts int) EmailDelivery {
		t.Helper()
		items, total, err := e.svc.EmailDeliveries(ctx, p, query)
		if err != nil || total != 1 || len(items) != 1 {
			t.Fatalf("query=%+v total=%d err=%v", items, total, err)
		}
		d := items[0]
		if d.Status != status || d.Attempts != attempts || !d.ForceEmail || d.Title != in.Title {
			t.Fatalf("delivery=%+v", d)
		}
		return d
	}
	check("pending", 0)
	if _, err := e.pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, u); err != nil {
		t.Fatal(err)
	}
	e.smtp.FailNext.Store(1)
	e.process()
	d := check("pending", 1)
	if d.LastError == nil || d.NextAttemptAt == nil || d.SentAt != nil {
		t.Fatalf("retry details=%+v", d)
	}
	e.clk.add(time.Minute)
	e.process()
	d = check("sent", 2)
	if d.SentAt == nil || d.LastError != nil || d.NextAttemptAt != nil {
		t.Fatalf("sent details=%+v", d)
	}
	// Idempotent retries keep the event identifier, including on a suspended user.
	again, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{})
	if err != nil || !again.AlreadySent || again.EventID == nil || *again.EventID != *sent.EventID {
		t.Fatalf("repeat=%+v %v", again, err)
	}
	// An explicitly addressed new forced notice also reaches a suspended user.
	in.IdempotencyKey = uuid.New()
	preview, err := e.svc.SendMessage(ctx, p, in, true, RequestMeta{})
	if err != nil || preview.Recipients != 1 || preview.EmailQueued != 1 {
		t.Fatalf("disabled preview=%+v %v", preview, err)
	}
	if _, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	e.process()
	if msgs := e.smtp.Wait(2, time.Second); len(msgs) != 2 {
		t.Fatalf("messages=%+v", msgs)
	}
	// Ordinary notifications keep the suspension check.
	if !e.emit(ev(TypeKeyExpiring, "ordinary-disabled", u)) {
		t.Fatal("event not recorded")
	}
	items, total, err := e.svc.EmailDeliveries(ctx, p, EmailDeliveryQuery{UserID: &u, Limit: 20})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("ordinary mail was queued: %+v %d %v", items, total, err)
	}
}

func TestEmailDeliveryQueryFiltersAndPermissions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("sender", identity.RoleSystemAdmin)
	u := e.user("recipient", identity.RoleUser)
	p := e.principal(admin, identity.RoleSystemAdmin)
	for i := 0; i < 3; i++ {
		in := SendMessageInput{Target: MessageTarget{UserIDs: []uuid.UUID{u}}, Title: "公告", Body: "正文", ForceEmail: true, IdempotencyKey: uuid.New()}
		if _, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	first, total, err := e.svc.EmailDeliveries(ctx, p, EmailDeliveryQuery{UserID: &u, Status: "pending", Limit: 1})
	if err != nil || total != 3 || len(first) != 1 {
		t.Fatalf("page1=%+v %d %v", first, total, err)
	}
	second, total, err := e.svc.EmailDeliveries(ctx, p, EmailDeliveryQuery{UserID: &u, Status: "pending", Offset: 1, Limit: 1})
	if err != nil || total != 3 || len(second) != 1 || first[0].ID == second[0].ID {
		t.Fatalf("page2=%+v %d %v", second, total, err)
	}
	for _, q := range []EmailDeliveryQuery{{UserID: &admin, Limit: 20}, {Status: "sent", Limit: 20}, {EventID: ptrUUID(uuid.New()), Limit: 20}} {
		items, total, err := e.svc.EmailDeliveries(ctx, p, q)
		if err != nil || total != 0 || len(items) != 0 {
			t.Fatalf("unexpected match %+v %d %v", items, total, err)
		}
	}
	if _, _, err := e.svc.EmailDeliveries(ctx, p, EmailDeliveryQuery{Status: "unknown"}); err == nil {
		t.Fatal("invalid status accepted")
	}
	if _, _, err := e.svc.EmailDeliveries(ctx, e.principal(u, identity.RoleUser), EmailDeliveryQuery{}); err == nil {
		t.Fatal("ordinary user can read deliveries")
	}
	if st := e.statuses(u); st["event:pending"] != 3 || len(st) != 1 {
		t.Fatalf("query mutated deliveries: %v", st)
	}
}

func ptrUUID(id uuid.UUID) *uuid.UUID { return &id }

func TestForcedMailStillChecksSMTPAndVerifiedEmail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("sender", identity.RoleSystemAdmin)
	u := e.user("recipient", identity.RoleUser)
	p := e.principal(admin, identity.RoleSystemAdmin)
	in := SendMessageInput{Target: MessageTarget{UserIDs: []uuid.UUID{u}}, Title: "公告", Body: "正文", ForceEmail: true, IdempotencyKey: uuid.New()}
	if _, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, u); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE user_identities SET email_verified = false WHERE user_id = $1`, u); err != nil {
		t.Fatal(err)
	}
	e.process()
	items, total, err := e.svc.EmailDeliveries(ctx, p, EmailDeliveryQuery{UserID: &u, Limit: 20})
	if err != nil || total != 1 || items[0].Status != "skipped" || items[0].LastError == nil || *items[0].LastError != "没有已验证的邮箱地址" {
		t.Fatalf("unverified=%+v %d %v", items, total, err)
	}
	r := &recipient{user: &userInfo{Status: identity.StatusDisabled}, prefs: defaultStored(u), email: "verified@example.com"}
	e.set.enabled = false
	if why := e.svc.emailBlockedForEvent(ctx, r, TypeAdminMessage, true); why != "SMTP 未配置或通知已关闭" {
		t.Fatalf("SMTP bypassed: %q", why)
	}
	if forcedEmail(TypeKeyExpiring, map[string]any{"forceEmail": true}) {
		t.Fatal("ordinary event may bypass suspension")
	}
}

func TestEmailDeliveryQuerySummaryAndWebhookExclusion(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("sender", identity.RoleSystemAdmin)
	u := e.user("recipient", identity.RoleUser)
	now := e.clk.now()
	for _, channel := range []string{"email", "webhook"} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO notification_deliveries (id,user_id,channel,kind,type,status,next_attempt_at,sent_at,created_at,updated_at)
			VALUES ($1,$2,$3,'summary','summary','sent',$4,$4,$4,$4)`, uuid.New(), u, channel, now); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := e.svc.EmailDeliveries(ctx, e.principal(admin, identity.RoleSystemAdmin), EmailDeliveryQuery{UserID: &u, Limit: 20})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("query=%+v %d %v", items, total, err)
	}
	d := items[0]
	if d.Kind != "summary" || d.EventID != nil || d.ForceEmail || d.Title != "" || d.Status != "sent" || d.SentAt == nil {
		t.Fatalf("summary=%+v", d)
	}
}

func TestOrdinaryQueuedMailStillSkippedAfterSuspension(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user("recipient", identity.RoleUser)
	e.emit(ev(TypeKeyExpiring, "ordinary-before-suspension", u))
	if _, err := e.pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, u); err != nil {
		t.Fatal(err)
	}
	e.process()
	if st := e.statuses(u); st["event:skipped"] != 1 || len(st) != 1 {
		t.Fatalf("ordinary delivery=%v", st)
	}
}
