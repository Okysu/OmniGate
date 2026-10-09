package identity

import (
	"context"
	"crypto/sha256"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/platform/db"
)

// Store is the PostgreSQL repository for users, identities and sessions.
type Store struct {
	pool *db.DB
}

func NewStore(pool *db.DB) *Store { return &Store{pool: pool} }

// userCols also works unqualified (strings.ReplaceAll(userCols, "u.", "") in
// RETURNING clauses): the group name subquery then resolves group_id to the
// users row.
const userCols = `u.id, u.display_name, u.email, u.avatar_url, u.role, u.status, u.version, u.created_at, u.last_login_at,
	u.disabled_reason, u.disabled_until, u.group_id, (SELECT g.name FROM user_groups g WHERE g.id = u.group_id)`

func (u *User) scanDest() []any {
	return []any{&u.ID, &u.DisplayName, &u.Email, &u.AvatarURL, &u.Role, &u.Status, &u.Version, &u.CreatedAt, &u.LastLoginAt,
		&u.DisabledReason, &u.DisabledUntil, &u.groupID, &u.groupName}
}

func (u *User) normalize() {
	u.Identities = []Identity{}
	if u.groupID != nil {
		u.Group = &GroupRef{ID: *u.groupID}
		if u.groupName != nil {
			u.Group.Name = *u.groupName
		}
	}
	if u.DisabledUntil != nil {
		t := u.DisabledUntil.UTC()
		u.DisabledUntil = &t
	}
}

func scanUser(row db.Row) (*User, error) {
	var u User
	if err := row.Scan(u.scanDest()...); err != nil {
		return nil, err
	}
	u.normalize()
	return &u, nil
}

// FindByIdentity returns the user linked to (provider, subject), or nil.
func (s *Store) FindByIdentity(ctx context.Context, provider, subject string) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `
		SELECT `+userCols+` FROM users u
		JOIN user_identities i ON i.user_id = u.id
		WHERE i.provider = $1 AND i.subject = $2`, provider, subject))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return u, err
}

// CountUsers returns the number of user accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateWithIdentity creates a user and links the external identity atomically.
func (s *Store) CreateWithIdentity(ctx context.Context, ext External, role Role, now time.Time) (*User, error) {
	uid, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	iid, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	name := firstNonEmpty(ext.Name, ext.Login, ext.Email, "user-"+uid.String()[:8])
	var u *User
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `
			INSERT INTO users (id, display_name, email, avatar_url, role, created_at, updated_at, last_login_at, group_id)
			VALUES ($1, $2, $3, $4, $5, $6, $6, $6, (SELECT id FROM user_groups WHERE is_default))
			RETURNING `+strings.ReplaceAll(userCols, "u.", ""),
			uid, name, nullable(ext.Email), nullable(ext.AvatarURL), role, now))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO user_identities (id, user_id, provider, subject, login, email, email_verified, created_at, last_login_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`,
			iid, uid, ext.Provider, ext.Subject, nullable(ext.Login), nullable(ext.Email), ext.EmailVerified, now)
		return err
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return nil, apperr.New(apperr.KindConflict, "identity_conflict", "该外部账号已关联其他用户")
		}
		return nil, err
	}
	return u, nil
}

// RecordLogin refreshes mutable profile fields from the provider and login times.
func (s *Store) RecordLogin(ctx context.Context, userID uuid.UUID, ext External, now time.Time) error {
	return db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE user_identities SET login = $3, email = $4, email_verified = $5, last_login_at = $6
			WHERE provider = $1 AND subject = $2`,
			ext.Provider, ext.Subject, nullable(ext.Login), nullable(ext.Email), ext.EmailVerified, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE users SET last_login_at = $2,
				avatar_url = COALESCE($3, avatar_url),
				email = COALESCE(email, $4)
			WHERE id = $1`, userID, now, nullable(ext.AvatarURL), nullable(ext.Email))
		return err
	})
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE u.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("用户")
	}
	if err != nil {
		return nil, err
	}
	if err := s.attachIdentities(ctx, []*User{u}); err != nil {
		return nil, err
	}
	return u, nil
}

var userSorts = map[string]string{
	"createdAt":    "u.created_at ASC, u.id ASC",
	"-createdAt":   "u.created_at DESC, u.id DESC",
	"lastLoginAt":  "u.last_login_at ASC NULLS FIRST, u.id ASC",
	"-lastLoginAt": "u.last_login_at DESC NULLS LAST, u.id DESC",
	"displayName":  "lower(u.display_name) ASC, u.id ASC",
}

func (s *Store) List(ctx context.Context, q ListUsersQuery) ([]*User, int, error) {
	order, ok := userSorts[q.Sort]
	if !ok {
		order = userSorts["-createdAt"]
	}
	var conds []string
	args := []any{}
	if t := strings.TrimSpace(q.Q); t != "" {
		args = append(args, "%"+escapeLike(strings.ToLower(t))+"%")
		conds = append(conds, `(lower(u.display_name) LIKE $1 ESCAPE '\' OR lower(coalesce(u.email, '')) LIKE $1 ESCAPE '\'
			OR EXISTS (SELECT 1 FROM user_identities i WHERE i.user_id = u.id AND lower(coalesce(i.login, '')) LIKE $1 ESCAPE '\'))`)
	}
	if q.GroupID != nil {
		args = append(args, *q.GroupID)
		conds = append(conds, `u.group_id = $`+strconv.Itoa(len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users u `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users u `+where+
		` ORDER BY `+order+` LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	users := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return users, total, s.attachIdentities(ctx, users)
}

func (s *Store) attachIdentities(ctx context.Context, users []*User) error {
	if len(users) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(users))
	byID := make(map[uuid.UUID]*User, len(users))
	for i, u := range users {
		ids[i], byID[u.ID] = u.ID, u
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, provider, subject, login, email FROM user_identities
		WHERE user_id = ANY($1) ORDER BY created_at`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uid uuid.UUID
		var id Identity
		if err := rows.Scan(&uid, &id.Provider, &id.Subject, &id.Login, &id.Email); err != nil {
			return err
		}
		byID[uid].Identities = append(byID[uid].Identities, id)
	}
	return rows.Err()
}

// Patch applies an admin update if version matches. It returns the old and new user.
func (s *Store) Patch(ctx context.Context, id uuid.UUID, p UserPatch, now time.Time) (before, after *User, err error) {
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		before, err = scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE u.id = $1 FOR UPDATE`, id))
		if db.IsNoRows(err) {
			return apperr.NotFound("用户")
		}
		if err != nil {
			return err
		}
		if !p.AnyVersion && before.Version != p.Version {
			return apperr.VersionConflict()
		}
		role, status := before.Role, before.Status
		if p.Role != nil {
			role = *p.Role
		}
		if p.Status != nil {
			status = *p.Status
		}
		reason, until := before.DisabledReason, before.DisabledUntil
		switch {
		case status == StatusActive:
			reason, until = nil, nil
		case p.Status != nil:
			reason, until = &p.DisabledReason, p.DisabledUntil
		}
		// Never allow the system to end up without an active system admin.
		if before.Role == RoleSystemAdmin && before.Status == StatusActive &&
			(role != RoleSystemAdmin || status != StatusActive) {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM users
				WHERE role = 'system_admin' AND status = 'active' AND id <> $1`, id).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return apperr.New(apperr.KindConflict, "last_admin", "不能降级或停用最后一位系统管理员")
			}
		}
		after, err = scanUser(tx.QueryRow(ctx, `
			UPDATE users SET role = $2, status = $3, disabled_reason = $5, disabled_until = $6, version = version + 1, updated_at = $4
			WHERE id = $1 RETURNING `+strings.ReplaceAll(userCols, "u.", ""), id, role, status, now, reason, until))
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return before, after, s.attachIdentities(ctx, []*User{after})
}

// ---- sessions ----

// HashToken returns the stored form of a session token.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func (s *Store) CreateSession(ctx context.Context, userID uuid.UUID, token string, ua, ipPrefix string, now, expires time.Time) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	if len(ua) > 512 {
		ua = ua[:512]
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, created_at, last_seen_at, expires_at, user_agent, ip_prefix)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $7)`, id, userID, HashToken(token), now, expires, ua, ipPrefix)
	return id, err
}

// SessionUser is the result of authenticating a session token.
type SessionUser struct {
	SessionID  uuid.UUID
	LastSeenAt time.Time
	User       *User
}

// LookupSession resolves a token to an active session and user. The idle timeout
// is enforced against last_seen_at.
func (s *Store) LookupSession(ctx context.Context, token string, now time.Time, idle time.Duration) (*SessionUser, error) {
	var su SessionUser
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT s.id, s.last_seen_at, `+userCols+`
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > $2 AND s.last_seen_at > $3`,
		HashToken(token), now, now.Add(-idle)).
		Scan(append([]any{&su.SessionID, &su.LastSeenAt}, u.scanDest()...)...)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.normalize()
	su.User = &u
	return &su, nil
}

func (s *Store) TouchSession(ctx context.Context, id uuid.UUID, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id = $1`, id, now)
	return err
}

func (s *Store) ListSessions(ctx context.Context, userID uuid.UUID, now time.Time, idle time.Duration) ([]Session, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, created_at, last_seen_at, expires_at, user_agent, ip_prefix FROM sessions
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > $2 AND last_seen_at > $3
		ORDER BY last_seen_at DESC`, userID, now, now.Add(-idle))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var ss Session
		if err := rows.Scan(&ss.ID, &ss.UserID, &ss.CreatedAt, &ss.LastSeenAt, &ss.ExpiresAt, &ss.UserAgent, &ss.IPPrefix); err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// RevokeSession revokes one of the user's sessions. Returns NotFound if it isn't theirs.
func (s *Store) RevokeSession(ctx context.Context, userID, sessionID uuid.UUID, reason string, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = $3, revoked_reason = $4
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, sessionID, userID, now, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("会话")
	}
	return nil
}

// RevokeOtherSessions revokes all of a user's sessions except keep (uuid.Nil keeps none).
func (s *Store) RevokeOtherSessions(ctx context.Context, userID, keep uuid.UUID, reason string, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = $3, revoked_reason = $4
		WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`, userID, keep, now, reason)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// EnableDue enables every disabled user whose suspension ended at or before
// now (the auto-enable job) and returns them as they were before.
func (s *Store) EnableDue(ctx context.Context, now time.Time) ([]*User, error) {
	var out []*User
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		out = nil
		rows, err := tx.Query(ctx, `SELECT `+userCols+` FROM users u
			WHERE u.status = 'disabled' AND u.disabled_until IS NOT NULL AND u.disabled_until <= $1
			ORDER BY u.disabled_until, u.id LIMIT 500 FOR UPDATE`, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				return err
			}
			out = append(out, u)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		for _, u := range out {
			if _, err := tx.Exec(ctx, `UPDATE users SET status = 'active', disabled_reason = NULL, disabled_until = NULL,
				version = version + 1, updated_at = $2 WHERE id = $1`, u.ID, now); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// IdentityDetails returns the user's linked identities, oldest first.
func (s *Store) IdentityDetails(ctx context.Context, userID uuid.UUID) ([]IdentityDetail, error) {
	rows, err := s.pool.Query(ctx, `SELECT provider, subject, email, created_at FROM user_identities
		WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IdentityDetail{}
	for rows.Next() {
		var d IdentityDetail
		if err := rows.Scan(&d.Provider, &d.Subject, &d.Email, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CountActiveSessions counts the user's usable sessions at now.
func (s *Store) CountActiveSessions(ctx context.Context, userID uuid.UUID, now time.Time, idle time.Duration) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sessions
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > $2 AND last_seen_at > $3`, userID, now, now.Add(-idle)).Scan(&n)
	return n, err
}
