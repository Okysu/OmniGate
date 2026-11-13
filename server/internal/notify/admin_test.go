package notify

import (
	"context"
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
