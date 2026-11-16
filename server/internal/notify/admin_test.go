package notify

import (
	"context"
	"fmt"
	"omnigate/internal/subscription"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"omnigate/internal/identity"
)

func TestSendMessageTargetsAndIdempotency(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("sender", identity.RoleSystemAdmin)
	a, b := e.user("alice", identity.RoleUser), e.user("bob", identity.RoleUser)
	disabled := e.user("disabled", identity.RoleUser)
	group := uuid.New()
	if _, err := e.pool.Exec(ctx, `INSERT INTO user_groups (id, name) VALUES ($1, 'supporters')`, group); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []uuid.UUID{a, b, disabled} {
		if _, err := e.pool.Exec(ctx, `UPDATE users SET group_id = $2 WHERE id = $1`, uid, group); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, disabled); err != nil {
		t.Fatal(err)
	}
	in := SendMessageInput{Target: MessageTarget{UserIDs: []uuid.UUID{a, a}, GroupIDs: []uuid.UUID{group}}, Title: "支持者福利", Body: "感谢支持！余额已到账。", Link: "/console/billing", IdempotencyKey: uuid.New()}
	p := e.principal(admin, identity.RoleSystemAdmin)
	preview, err := e.svc.SendMessage(ctx, p, in, true, RequestMeta{})
	if err != nil || preview.Recipients != 2 {
		t.Fatalf("preview %+v %v", preview, err)
	}
	var count int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&count)
	if count != 0 {
		t.Fatal("dry run sent notifications")
	}
	if _, err := e.svc.SendMessage(ctx, e.principal(a, identity.RoleUser), in, false, RequestMeta{}); err == nil {
		t.Fatal("user can send admin notifications")
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{}); err != nil {
				t.Errorf("send: %v", err)
			}
		})
	}
	wg.Wait()
	for _, uid := range []uuid.UUID{a, b} {
		items, n, err := e.svc.List(ctx, uid, ListQuery{Limit: 20})
		if err != nil || n != 1 || len(items) != 1 || items[0].Title != in.Title {
			t.Fatalf("notification uid=%s n=%d items=%+v err=%v", uid, n, items, err)
		}
	}
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1`, disabled).Scan(&count)
	if count != 0 {
		t.Fatal("disabled user received notice")
	}
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'notification.send'`).Scan(&count)
	if count != 1 {
		t.Fatalf("audit count %d", count)
	}
	again, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{})
	if err != nil || !again.AlreadySent {
		t.Fatalf("repeat %+v %v", again, err)
	}
	in.Body = "different"
	if _, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{}); err == nil {
		t.Fatal("idempotency key accepted different payload")
	}
}

func TestSendMessageValidation(t *testing.T) {
	for _, mutate := range []func(*SendMessageInput){
		func(i *SendMessageInput) { i.Target = MessageTarget{} },
		func(i *SendMessageInput) { i.Title = "" },
		func(i *SendMessageInput) { i.Body = strings.Repeat("字", 10001) },
		func(i *SendMessageInput) { i.Link = "//evil.example" },
		func(i *SendMessageInput) { i.Link = "/\\evil.example" },
		func(i *SendMessageInput) { i.IdempotencyKey = uuid.Nil },
	} {
		in := SendMessageInput{Target: MessageTarget{UserIDs: []uuid.UUID{uuid.New()}}, Title: "title", Body: "body", IdempotencyKey: uuid.New()}
		mutate(&in)
		if validateMessage(&in) == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
}

func TestNotificationSharedAudiencePreview(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("admin", identity.RoleSystemAdmin)
	u := e.user("recipient", identity.RoleUser)
	filter := subscription.AudienceFilter{UserIDs: []uuid.UUID{u}, MinSpend: "0"}
	expected, err := subscription.PreviewAudience(ctx, e.pool, filter, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	in := SendMessageInput{Target: MessageTarget{Filter: &filter}, Title: "Preview", Body: "Preview only", IdempotencyKey: uuid.New()}
	result, err := e.svc.SendMessage(ctx, e.principal(admin, identity.RoleSystemAdmin), in, true, RequestMeta{})
	if err != nil || result.Recipients != expected.Users || result.Recipients != 1 {
		t.Fatalf("preview=%+v %v", result, err)
	}
	in.Target.UserIDs = []uuid.UUID{u}
	if _, err = e.svc.SendMessage(ctx, e.principal(admin, identity.RoleSystemAdmin), in, true, RequestMeta{}); err == nil {
		t.Fatal("mixed union and intersection accepted")
	}
}

func TestForcedMessageEmailFanoutAndRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("sender", identity.RoleSystemAdmin)
	u := e.user("optout", identity.RoleUser)
	unverified := e.user("unverified", identity.RoleUser)
	disabled := e.user("disabled", identity.RoleUser)
	if _, err := e.pool.Exec(ctx, `UPDATE user_identities SET email_verified = false WHERE user_id = $1`, unverified); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, disabled); err != nil {
		t.Fatal(err)
	}
	e.putPrefs(u, identity.RoleUser, `{"version":0,"email":{"enabled":false},"events":{"admin.message":{"email":false,"inApp":true}},"digest":"daily"}`)
	p := e.principal(admin, identity.RoleSystemAdmin)
	in := SendMessageInput{Target: MessageTarget{Filter: &subscription.AudienceFilter{}}, Title: "重要公告", Body: "强制邮件正文", ForceEmail: true, IdempotencyKey: uuid.New()}
	if _, err := e.svc.SendMessage(ctx, e.principal(u, identity.RoleUser), in, false, RequestMeta{}); err == nil {
		t.Fatal("ordinary user can force email")
	}
	preview, err := e.svc.SendMessage(ctx, p, in, true, RequestMeta{})
	if err != nil || preview.Recipients != 3 || preview.EmailQueued != 2 || preview.EmailSkipped != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	var count int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM notification_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview wrote events: %d %v", count, err)
	}
	res, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{})
	if err != nil || res.Recipients != preview.Recipients || res.EmailQueued != 2 || res.EmailSkipped != 1 {
		t.Fatalf("send=%+v err=%v", res, err)
	}
	// A fresh worker must recover the override from the persisted event.
	e.svc, err = New(e.svc.opts)
	if err != nil {
		t.Fatal(err)
	}
	e.smtp.FailNext.Store(1)
	e.process()
	e.clk.add(time.Minute)
	e.process()
	msgs := e.smtp.Wait(2, time.Second)
	if len(msgs) != 2 {
		t.Fatalf("messages=%+v", msgs)
	}
	found := false
	for _, msg := range msgs {
		if msg.To[0] == "optout@example.com" {
			found = true
		}
		if msg.Header.Get("List-Unsubscribe") != "" || !strings.Contains(msg.Text, in.Body) {
			t.Fatalf("forced mail=%+v", msg)
		}
	}
	if !found {
		t.Fatal("opted-out user received no email")
	}
	if st := e.statuses(unverified); len(st) != 0 {
		t.Fatalf("unverified delivery: %v", st)
	}
	if st := e.statuses(disabled); len(st) != 0 {
		t.Fatalf("disabled delivery: %v", st)
	}
	prefs, err := e.svc.GetPreferences(ctx, e.principal(u, identity.RoleUser))
	if err != nil || prefs.Email.Enabled || prefs.Events[TypeAdminMessage].Email || prefs.Digest != "daily" {
		t.Fatalf("preferences changed: %+v %v", prefs, err)
	}
	again, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{})
	if err != nil || !again.AlreadySent || again.EmailQueued != 2 || again.EmailSkipped != 1 {
		t.Fatalf("repeat=%+v %v", again, err)
	}
	in.ForceEmail = false
	if _, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{}); err == nil {
		t.Fatal("same key accepted different force policy")
	}
	in.IdempotencyKey = uuid.New()
	in.Target = MessageTarget{UserIDs: []uuid.UUID{u}}
	ordinary, err := e.svc.SendMessage(ctx, p, in, false, RequestMeta{})
	if err != nil || ordinary.EmailQueued != 0 || ordinary.EmailSkipped != 1 {
		t.Fatalf("ordinary=%+v %v", ordinary, err)
	}
}

func TestForcedMessageRateLimitDefersWithoutDigest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("sender", identity.RoleSystemAdmin)
	u := e.user("recipient", identity.RoleUser)
	e.set.limit = 1
	e.putPrefs(u, identity.RoleUser, `{"version":0,"email":{"enabled":false},"digest":"daily"}`)
	in := SendMessageInput{Target: MessageTarget{UserIDs: []uuid.UUID{u}}, Title: "公告", Body: "正文", ForceEmail: true}
	for range 2 {
		in.IdempotencyKey = uuid.New()
		if _, err := e.svc.SendMessage(ctx, e.principal(admin, identity.RoleSystemAdmin), in, false, RequestMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	e.process()
	if st := e.statuses(u); st["event:sent"] != 1 || st["event:pending"] != 1 || len(st) != 2 {
		t.Fatalf("rate limited=%v", st)
	}
	e.clk.add(time.Hour)
	e.process()
	if st := e.statuses(u); st["event:sent"] != 2 || len(st) != 1 {
		t.Fatalf("deferred=%v", st)
	}
	if msgs := e.smtp.Wait(2, time.Second); len(msgs) != 2 {
		t.Fatalf("messages=%+v", msgs)
	}
}

func TestForcedMessageRequiresSMTP(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			admin := e.user("sender", identity.RoleSystemAdmin)
			if disabled {
				e.set.enabled = false
			} else {
				e.set.smtp.Host = ""
			}
			in := SendMessageInput{Target: MessageTarget{UserIDs: []uuid.UUID{admin}}, Title: "公告", Body: "正文", ForceEmail: true, IdempotencyKey: uuid.New()}
			for _, dry := range []bool{true, false} {
				if _, err := e.svc.SendMessage(ctx, e.principal(admin, identity.RoleSystemAdmin), in, dry, RequestMeta{}); err == nil {
					t.Fatal("force email accepted without SMTP")
				}
			}
			var n int
			if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM notification_events`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("events=%d %v", n, err)
			}
		})
	}
}
