package notify

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/identity"
	"omnigate/internal/platform/db"
)

// Event is one notification event (§2).
type Event struct {
	Type string
	// Key is the dedupe key: an event with a key that was already emitted is
	// ignored (unique constraint), which makes every producer idempotent.
	Key string
	// Subject groups events for Throttle (e.g. the channel id).
	Subject string
	// Throttle, when > 0, drops the event if one of the same Type and Subject
	// was emitted within this window.
	Throttle time.Duration
	Severity string // default: the catalog severity
	Title    string
	Body     string
	// Link is an in-site path (e.g. /console/billing).
	Link string
	Data map[string]any
	// Users are the intended recipients; users that are disabled (except for
	// account.* events) or not eligible for the type are dropped.
	Users []uuid.UUID
}

const recentKeys = 20000

// seen reports whether key was emitted recently by this instance.
func (s *Service) seen(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.recent[key]
	return ok
}

func (s *Service) remember(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recent) >= recentKeys {
		s.recent = map[string]time.Time{}
	}
	s.recent[key] = time.Now()
}

// Emit records ev and fans it out to its recipients: in-app notifications and
// email / webhook deliveries according to their preferences. It returns
// whether the event was new.
func (s *Service) Emit(ctx context.Context, ev Event) (bool, error) {
	meta, ok := Meta(ev.Type)
	if !ok || ev.Key == "" {
		return false, errors.New("notify: unknown event type or empty dedupe key")
	}
	if s.seen(ev.Key) {
		return false, nil
	}
	now := s.now()
	if ev.Throttle > 0 {
		var recent bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notification_events WHERE type = $1 AND subject = $2 AND created_at > $3)`,
			ev.Type, ev.Subject, now.Add(-ev.Throttle)).Scan(&recent); err != nil {
			return false, err
		}
		if recent {
			return false, nil
		}
	}
	if ev.Severity == "" {
		ev.Severity = meta.Severity
	}
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	recipients, err := s.users(ctx, dedupeIDs(ev.Users))
	if err != nil {
		return false, err
	}
	var targets []*userInfo
	for _, u := range recipients {
		if u.Status != identity.StatusActive && !ReachesDisabled(ev.Type) {
			continue
		}
		el := Eligibility{Role: u.Role, OwnsChannel: true} // channel recipients are resolved by the producer
		if el.Eligible(ev.Type) {
			targets = append(targets, u)
		}
	}
	ids := make([]uuid.UUID, len(targets))
	for i, u := range targets {
		ids[i] = u.ID
	}
	prefs, err := s.loadPrefsMany(ctx, ids)
	if err != nil {
		return false, err
	}
	outbound := s.set.NotificationsEnabled(ctx)
	emailOK := outbound && s.set.SMTP(ctx).Configured()
	data, _ := json.Marshal(ev.Data)
	var link *string
	if ev.Link != "" {
		link = &ev.Link
	}
	type count struct{ channel, result string }
	var counts []count
	inserted := false
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		counts, inserted = nil, false
		eid := newID()
		err := tx.QueryRow(ctx, `INSERT INTO notification_events (id, type, dedupe_key, subject, severity, title, body, link, data, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (dedupe_key) DO NOTHING RETURNING id`,
			eid, ev.Type, ev.Key, ev.Subject, ev.Severity, ev.Title, ev.Body, link, data, now).Scan(&eid)
		if db.IsNoRows(err) {
			return nil // already emitted
		}
		if err != nil {
			return err
		}
		inserted = true
		for _, u := range targets {
			p := prefs[u.ID]
			sw := p.switches(ev.Type)
			if sw.InApp {
				if _, err := tx.Exec(ctx, `INSERT INTO notifications (id, user_id, event_id, type, severity, created_at)
					VALUES ($1, $2, $3, $4, $5, $6)`, newID(), u.ID, eid, ev.Type, ev.Severity, now); err != nil {
					return err
				}
				counts = append(counts, count{"inapp", resultSent})
			}
			if sw.Email && p.EmailEnabled {
				if !emailOK {
					counts = append(counts, count{"email", resultSkipped})
				} else {
					kind, due := "event", now
					if p.Digest == "daily" && !meta.Alert {
						kind, due = "digest", nextDigest(now, p.location())
					}
					if err := insertDelivery(ctx, tx, u.ID, &eid, "email", kind, ev.Type, due, now); err != nil {
						return err
					}
				}
			}
			if sw.Webhook && p.WebhookEnabled && p.WebhookURL != nil {
				if !outbound {
					counts = append(counts, count{"webhook", resultSkipped})
				} else if err := insertDelivery(ctx, tx, u.ID, &eid, "webhook", "event", ev.Type, now, now); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	s.remember(ev.Key)
	for _, c := range counts {
		metricSent.WithLabelValues(c.channel, ev.Type, c.result).Inc()
	}
	if inserted {
		s.kick()
	}
	return inserted, nil
}

func insertDelivery(ctx context.Context, tx db.Tx, uid uuid.UUID, eventID *uuid.UUID, channel, kind, typ string, due, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO notification_deliveries (id, user_id, event_id, channel, kind, type, status, next_attempt_at,
			created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8, $8)`, newID(), uid, eventID, channel, kind, typ, due, now)
	return err
}

// nextDigest is the next 09:00 in loc strictly after now.
func nextDigest(now time.Time, loc *time.Location) time.Time {
	l := now.In(loc)
	t := time.Date(l.Year(), l.Month(), l.Day(), 9, 0, 0, 0, loc)
	if !t.After(now) {
		t = time.Date(l.Year(), l.Month(), l.Day()+1, 9, 0, 0, 0, loc)
	}
	return t.UTC()
}

// emitLogged emits ev and logs failures (background producers).
func (s *Service) emitLogged(ctx context.Context, ev Event) {
	if _, err := s.Emit(ctx, ev); err != nil {
		s.log.Error("notification emit failed", "type", ev.Type, "key", ev.Key, "err", err)
	}
}

func dedupeIDs(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, id := range ids {
		if id != uuid.Nil && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ---- scanner state (re-arm marks, known sets) ----

// setMark creates key and reports whether it was absent.
func (s *Service) setMark(ctx context.Context, key string, value any) (bool, error) {
	v, _ := json.Marshal(value)
	tag, err := s.pool.Exec(ctx, `INSERT INTO notification_state (key, value, updated_at) VALUES ($1, $2, $3) ON CONFLICT (key) DO NOTHING`,
		key, v, s.now())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// clearMark deletes key (re-arms the alert it guards).
func (s *Service) clearMark(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM notification_state WHERE key = $1`, key)
	return err
}

// putState stores value under key.
func (s *Service) putState(ctx context.Context, key string, value any) error {
	v, _ := json.Marshal(value)
	_, err := s.pool.Exec(ctx, `INSERT INTO notification_state (key, value, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`, key, v, s.now())
	return err
}

// getState loads key into dst; it reports whether the key exists.
func (s *Service) getState(ctx context.Context, key string, dst any) (bool, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM notification_state WHERE key = $1`, key).Scan(&raw)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, dst)
}
