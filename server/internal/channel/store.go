package channel

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/secretbox"
)

// Store persists channels. Secrets are sealed with the "channel-secret" keyring
// and bound to (channel id, field) via associated data.
type Store struct {
	pool *db.DB
	box  *secretbox.Keyring
}

func NewStore(pool *db.DB, box *secretbox.Keyring) *Store { return &Store{pool: pool, box: box} }

func secretAD(id uuid.UUID, field string) string {
	return "channel_secret:" + id.String() + ":" + field
}

func hint(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 8 {
		return "…"
	}
	return "…" + key[len(key)-4:]
}

const channelCols = `c.id, c.name, c.type, c.base_url, c.config, c.scope, c.status, c.priority, c.weight, c.version,
	c.created_at, c.updated_at, c.owner_id, u.display_name, s.hint, c.plugin_version_id, c.plugin_config,
	pp.id, pp.plugin_key, pp.name, pv.version, c.alert_balance_below`

const channelFrom = ` FROM channels c JOIN users u ON u.id = c.owner_id
	LEFT JOIN channel_secrets s ON s.channel_id = c.id AND s.field = 'api_key'
	LEFT JOIN plugin_versions pv ON pv.id = c.plugin_version_id
	LEFT JOIN plugins pp ON pp.id = pv.plugin_id`

// pluginSecretPrefix namespaces plugin-declared secret fields in channel_secrets.
const pluginSecretPrefix = "plugin:"

func scanChannel(row db.Row, extra ...any) (*Channel, error) {
	var c Channel
	var cfg, pcfg []byte
	var h *string
	var pid *uuid.UUID
	var pkey, pname, pver *string
	dest := append([]any{&c.ID, &c.Name, &c.Type, &c.BaseURL, &cfg, &c.Scope, &c.Status, &c.Priority, &c.Weight, &c.Version,
		&c.CreatedAt, &c.UpdatedAt, &c.Owner.ID, &c.Owner.DisplayName, &h, &c.PluginVersionID, &pcfg, &pid, &pkey, &pname, &pver, &c.Alerts.BalanceBelow}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(cfg, &c.Config)
	c.PluginConfig = map[string]any{}
	_ = json.Unmarshal(pcfg, &c.PluginConfig)
	c.Secret = SecretInfo{Set: h != nil, Hint: h}
	c.Models, c.SharedWith, c.SecretFields = []ModelMap{}, Sharing{Users: []uuid.UUID{}, Groups: []uuid.UUID{}}, map[string]SecretInfo{}
	c.Shares = []ChannelShare{}
	if c.PluginVersionID != nil && pid != nil {
		c.Plugin = &PluginRef{ID: *pid, Key: *pkey, Name: *pname, Version: *pver, VersionID: *c.PluginVersionID}
	}
	return &c, nil
}

// attach loads models and shares for the given channels.
func (s *Store) attach(ctx context.Context, q db.Querier, list []*Channel) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(list))
	byID := map[uuid.UUID]*Channel{}
	for i, c := range list {
		ids[i], byID[c.ID] = c.ID, c
	}
	rows, err := q.Query(ctx, `SELECT channel_id, model, upstream_model, upstream_protocol FROM channel_models
		WHERE channel_id = ANY($1) ORDER BY channel_id, position, model`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		var m ModelMap
		if err := rows.Scan(&id, &m.Model, &m.UpstreamModel, &m.UpstreamProtocol); err != nil {
			rows.Close()
			return err
		}
		byID[id].Models = append(byID[id].Models, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = q.Query(ctx, `SELECT channel_id, field, hint FROM channel_secrets WHERE channel_id = ANY($1) AND field LIKE 'plugin:%'`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		var field string
		var h *string
		if err := rows.Scan(&id, &field, &h); err != nil {
			rows.Close()
			return err
		}
		byID[id].SecretFields[strings.TrimPrefix(field, pluginSecretPrefix)] = SecretInfo{Set: true, Hint: h}
	}
	rows.Close()
	if err := s.attachShares(ctx, q, list); err != nil {
		return err
	}
	rows, err = q.Query(ctx, `SELECT channel_id, group_id FROM channel_group_shares WHERE channel_id = ANY($1) ORDER BY created_at, group_id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, gid uuid.UUID
		if err := rows.Scan(&id, &gid); err != nil {
			return err
		}
		byID[id].SharedWith.Groups = append(byID[id].SharedWith.Groups, gid)
	}
	return rows.Err()
}

// attachShares (re)loads the user shares of list: Shares with every invited
// user and SharedWith.Users with the pending and accepted ones.
func (s *Store) attachShares(ctx context.Context, q db.Querier, list []*Channel) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(list))
	byID := map[uuid.UUID]*Channel{}
	for i, c := range list {
		ids[i], byID[c.ID] = c.ID, c
		c.Shares, c.SharedWith.Users = []ChannelShare{}, []uuid.UUID{}
	}
	rows, err := q.Query(ctx, `SELECT s.channel_id, s.user_id, u.display_name, s.status, s.created_at, s.responded_at
		FROM channel_shares s JOIN users u ON u.id = s.user_id WHERE s.channel_id = ANY($1) ORDER BY s.created_at, s.user_id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var sh ChannelShare
		if err := rows.Scan(&id, &sh.UserID, &sh.DisplayName, &sh.Status, &sh.CreatedAt, &sh.RespondedAt); err != nil {
			return err
		}
		c := byID[id]
		c.Shares = append(c.Shares, sh)
		if sh.Status != ShareDeclined {
			c.SharedWith.Users = append(c.SharedWith.Users, sh.UserID)
		}
	}
	return rows.Err()
}

// groupShareMembers returns, per channel shared with groups, the members of
// those groups (the registry expands group shares into users).
func (s *Store) groupShareMembers(ctx context.Context) (map[uuid.UUID][]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT gs.channel_id, u.id FROM channel_group_shares gs JOIN users u ON u.group_id = gs.group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID][]uuid.UUID{}
	for rows.Next() {
		var ch, uid uuid.UUID
		if err := rows.Scan(&ch, &uid); err != nil {
			return nil, err
		}
		out[ch] = append(out[ch], uid)
	}
	return out, rows.Err()
}

// userGroup returns the group of userID (nil when none).
func (s *Store) userGroup(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error) {
	var gid *uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT group_id FROM users WHERE id = $1`, userID).Scan(&gid)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return gid, err
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Channel, error) {
	c, err := scanChannel(s.pool.QueryRow(ctx, `SELECT `+channelCols+channelFrom+` WHERE c.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("渠道")
	}
	if err != nil {
		return nil, err
	}
	return c, s.attach(ctx, s.pool, []*Channel{c})
}

type ListQuery struct {
	Viewer uuid.UUID
	All    bool // caller holds channels.manage
	Q      string
	Type   string
	Scope  string
	Status string
	Offset int
	Limit  int
}

func (s *Store) List(ctx context.Context, q ListQuery) ([]*Channel, int, error) {
	var conds []string
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	if !q.All {
		v := arg(q.Viewer)
		conds = append(conds, `(c.owner_id = `+v+` OR c.scope = 'global' OR (c.scope = 'shared' AND (EXISTS
			(SELECT 1 FROM channel_shares sh WHERE sh.channel_id = c.id AND sh.user_id = `+v+` AND sh.status = 'accepted') OR EXISTS
			(SELECT 1 FROM channel_group_shares gs JOIN users gu ON gu.group_id = gs.group_id WHERE gs.channel_id = c.id AND gu.id = `+v+`))))`)
	}
	if t := strings.TrimSpace(q.Q); t != "" {
		p := arg("%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(t)) + "%")
		conds = append(conds, `(lower(c.name) LIKE `+p+` ESCAPE '\' OR EXISTS (SELECT 1 FROM channel_models m
			WHERE m.channel_id = c.id AND lower(m.model) LIKE `+p+` ESCAPE '\'))`)
	}
	for col, v := range map[string]string{"c.type": q.Type, "c.scope": q.Scope, "c.status": q.Status} {
		if v != "" {
			conds = append(conds, col+" = "+arg(v))
		}
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*)`+channelFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+channelCols+channelFrom+where+
		` ORDER BY c.priority DESC, c.created_at DESC LIMIT `+arg(q.Limit)+` OFFSET `+arg(q.Offset), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list := []*Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, 0, err
		}
		list = append(list, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return list, total, s.attach(ctx, s.pool, list)
}

// writeChildren replaces models, shares and (optionally) the API key.
func (s *Store) writeChildren(ctx context.Context, tx db.Tx, c *Channel, apiKey *string, secrets map[string]string) error {
	for name, v := range secrets {
		field := pluginSecretPrefix + name
		if v == "" {
			if _, err := tx.Exec(ctx, `DELETE FROM channel_secrets WHERE channel_id = $1 AND field = $2`, c.ID, field); err != nil {
				return err
			}
			delete(c.SecretFields, name)
			continue
		}
		ct, err := s.box.Seal([]byte(v), secretAD(c.ID, field))
		if err != nil {
			return err
		}
		h := hint(v)
		if _, err := tx.Exec(ctx, `INSERT INTO channel_secrets (channel_id, field, ciphertext, hint) VALUES ($1, $2, $3, $4)
			ON CONFLICT (channel_id, field) DO UPDATE SET ciphertext = EXCLUDED.ciphertext, hint = EXCLUDED.hint, updated_at = now()`,
			c.ID, field, ct, h); err != nil {
			return err
		}
		if c.SecretFields == nil {
			c.SecretFields = map[string]SecretInfo{}
		}
		c.SecretFields[name] = SecretInfo{Set: true, Hint: &h}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM channel_models WHERE channel_id = $1`, c.ID); err != nil {
		return err
	}
	for i, m := range c.Models {
		if _, err := tx.Exec(ctx, `INSERT INTO channel_models (channel_id, model, upstream_model, position, upstream_protocol) VALUES ($1, $2, $3, $4, $5)`,
			c.ID, m.Model, m.UpstreamModel, i, m.UpstreamProtocol); err != nil {
			return err
		}
	}
	if err := s.writeShares(ctx, tx, c); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM channel_group_shares WHERE channel_id = $1`, c.ID); err != nil {
		return err
	}
	for _, gid := range c.SharedWith.Groups {
		if _, err := tx.Exec(ctx, `INSERT INTO channel_group_shares (channel_id, group_id) VALUES ($1, $2)`, c.ID, gid); err != nil {
			if db.IsForeignKeyViolation(err) {
				return apperr.Validation("共享对象不存在", map[string]any{"sharedWith.groups": gid.String()})
			}
			return err
		}
	}
	if apiKey != nil {
		ct, err := s.box.Seal([]byte(strings.TrimSpace(*apiKey)), secretAD(c.ID, secretAPIKey))
		if err != nil {
			return err
		}
		h := hint(*apiKey)
		if _, err := tx.Exec(ctx, `INSERT INTO channel_secrets (channel_id, field, ciphertext, hint) VALUES ($1, $2, $3, $4)
			ON CONFLICT (channel_id, field) DO UPDATE SET ciphertext = EXCLUDED.ciphertext, hint = EXCLUDED.hint, updated_at = now()`,
			c.ID, secretAPIKey, ct, h); err != nil {
			return err
		}
		c.Secret = SecretInfo{Set: true, Hint: &h}
	}
	return nil
}

// writeShares applies c.SharedWith.Users to the user shares
// (phase5-api.md §5.2): new users and users who declined get a (new) pending
// invitation, recorded in c.invited; pending and accepted users that are no
// longer listed are removed; declined records stay while the channel is
// shared. c.Shares and c.SharedWith.Users are reloaded.
func (s *Store) writeShares(ctx context.Context, tx db.Tx, c *Channel) error {
	rows, err := tx.Query(ctx, `SELECT user_id, status FROM channel_shares WHERE channel_id = $1`, c.ID)
	if err != nil {
		return err
	}
	existing := map[uuid.UUID]string{}
	for rows.Next() {
		var uid uuid.UUID
		var st string
		if err := rows.Scan(&uid, &st); err != nil {
			rows.Close()
			return err
		}
		existing[uid] = st
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	want := map[uuid.UUID]bool{}
	for _, uid := range c.SharedWith.Users {
		want[uid] = true
	}
	for uid, st := range existing {
		if want[uid] || (st == ShareDeclined && c.Scope == authz.ScopeShared) {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM channel_shares WHERE channel_id = $1 AND user_id = $2`, c.ID, uid); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	c.invited = nil
	for _, uid := range c.SharedWith.Users {
		switch st, ok := existing[uid]; {
		case !ok:
			if _, err := tx.Exec(ctx, `INSERT INTO channel_shares (channel_id, user_id, status, created_at) VALUES ($1, $2, 'pending', $3)`,
				c.ID, uid, now); err != nil {
				if db.IsForeignKeyViolation(err) {
					return apperr.Validation("共享对象不存在", map[string]any{"sharedWith": uid.String()})
				}
				return err
			}
		case st == ShareDeclined:
			if _, err := tx.Exec(ctx, `UPDATE channel_shares SET status = 'pending', created_at = $3, responded_at = NULL
				WHERE channel_id = $1 AND user_id = $2`, c.ID, uid, now); err != nil {
				return err
			}
		default:
			continue
		}
		c.invited = append(c.invited, uid)
	}
	return s.attachShares(ctx, tx, []*Channel{c})
}

func (s *Store) Create(ctx context.Context, c *Channel, apiKey string, secrets map[string]string, after func(db.Tx) error) error {
	now := time.Now().UTC()
	c.ID, c.Version, c.CreatedAt, c.UpdatedAt = mustUUID(), 1, now, now
	cfg, _ := json.Marshal(c.Config)
	pcfg, _ := json.Marshal(c.PluginConfig)
	return db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO channels (id, owner_id, name, type, base_url, config, scope, status, priority, weight, created_at, updated_at,
			plugin_version_id, plugin_config, alert_balance_below) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, $12, $13, $14)`,
			c.ID, c.Owner.ID, c.Name, c.Type, c.BaseURL, cfg, c.Scope, c.Status, c.Priority, c.Weight, now, c.PluginVersionID, pcfg,
			c.Alerts.BalanceBelow); err != nil {
			return err
		}
		if err := s.writeChildren(ctx, tx, c, &apiKey, secrets); err != nil {
			return err
		}
		return after(tx)
	})
}

// Update saves c if its stored version still equals expectVersion.
func (s *Store) Update(ctx context.Context, c *Channel, expectVersion int, apiKey *string, secrets map[string]string, after func(db.Tx) error) error {
	cfg, _ := json.Marshal(c.Config)
	pcfg, _ := json.Marshal(c.PluginConfig)
	now := time.Now().UTC()
	return db.InTx(ctx, s.pool, func(tx db.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE channels SET name = $3, base_url = $4, config = $5, scope = $6, status = $7,
			priority = $8, weight = $9, version = version + 1, updated_at = $10, type = $11, plugin_version_id = $12, plugin_config = $13,
			alert_balance_below = $14
			WHERE id = $1 AND version = $2`,
			c.ID, expectVersion, c.Name, c.BaseURL, cfg, c.Scope, c.Status, c.Priority, c.Weight, now, c.Type, c.PluginVersionID, pcfg,
			c.Alerts.BalanceBelow)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.VersionConflict()
		}
		c.Version, c.UpdatedAt = expectVersion+1, now
		if err := s.writeChildren(ctx, tx, c, apiKey, secrets); err != nil {
			return err
		}
		return after(tx)
	})
}

func (s *Store) Delete(ctx context.Context, id uuid.UUID, after func(db.Tx) error) error {
	return db.InTx(ctx, s.pool, func(tx db.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM channels WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.NotFound("渠道")
		}
		return after(tx)
	})
}

// runtimeRow is a fully loaded, decrypted channel for the data plane.
type runtimeRow struct {
	Channel
	APIKey        string
	PluginSecrets map[string]string
	OwnerRole     string
	// OwnerActive: the owner is not disabled (shares of disabled owners do
	// not work, phase5-api.md §5.4).
	OwnerActive bool
}

// loadRuntime loads every enabled channel with its decrypted secrets.
func (s *Store) loadRuntime(ctx context.Context) ([]*runtimeRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+channelCols+`, u.role, u.status, s.ciphertext`+channelFrom+` WHERE c.status = 'enabled'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*runtimeRow
	var chans []*Channel
	for rows.Next() {
		var role, status string
		var ct *string
		c, err := scanChannel(rows, &role, &status, &ct)
		if err != nil {
			return nil, err
		}
		r := &runtimeRow{Channel: *c, OwnerRole: role, OwnerActive: status == string(identity.StatusActive), PluginSecrets: map[string]string{}}
		if ct != nil {
			pt, err := s.box.Open(*ct, secretAD(r.ID, secretAPIKey))
			if err != nil {
				// Unreadable secret (e.g. master key changed): skip channel, keep serving others.
				continue
			}
			r.APIKey = string(pt)
		}
		out = append(out, r)
		chans = append(chans, &r.Channel)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := s.attach(ctx, s.pool, chans); err != nil {
		return nil, err
	}
	// Decrypt plugin-declared secrets.
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(out))
	byID := map[uuid.UUID]*runtimeRow{}
	for i, r := range out {
		ids[i], byID[r.ID] = r.ID, r
	}
	srows, err := s.pool.Query(ctx, `SELECT channel_id, field, ciphertext FROM channel_secrets WHERE channel_id = ANY($1) AND field LIKE 'plugin:%'`, ids)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var id uuid.UUID
		var field, ct string
		if err := srows.Scan(&id, &field, &ct); err != nil {
			return nil, err
		}
		if pt, err := s.box.Open(ct, secretAD(id, field)); err == nil {
			byID[id].PluginSecrets[strings.TrimPrefix(field, pluginSecretPrefix)] = string(pt)
		}
	}
	return out, srows.Err()
}

// pluginSecrets returns decrypted plugin secret fields of one channel.
func (s *Store) pluginSecrets(ctx context.Context, id uuid.UUID) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT field, ciphertext FROM channel_secrets WHERE channel_id = $1 AND field LIKE 'plugin:%'`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var field, ct string
		if err := rows.Scan(&field, &ct); err != nil {
			return nil, err
		}
		if pt, err := s.box.Open(ct, secretAD(id, field)); err == nil {
			out[strings.TrimPrefix(field, pluginSecretPrefix)] = string(pt)
		}
	}
	return out, rows.Err()
}

// apiKey returns the decrypted key of one channel (for test/discover).
func (s *Store) apiKey(ctx context.Context, id uuid.UUID) (string, error) {
	var ct string
	err := s.pool.QueryRow(ctx, `SELECT ciphertext FROM channel_secrets WHERE channel_id = $1 AND field = $2`, id, secretAPIKey).Scan(&ct)
	if err != nil {
		return "", err
	}
	pt, err := s.box.Open(ct, secretAD(id, secretAPIKey))
	return string(pt), err
}

func (s *Store) ownerRole(ctx context.Context, owner uuid.UUID) (identity.Role, error) {
	var r identity.Role
	err := s.pool.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, owner).Scan(&r)
	return r, err
}

func mustUUID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}
	return id
}

// IncomingShare is a user share seen by its recipient (phase5-api.md §5.3):
// never the channel's address, configuration or secrets.
type IncomingShare struct {
	ChannelID     uuid.UUID  `json:"channelId"`
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	ChannelStatus string     `json:"channelStatus"`
	Owner         OwnerRef   `json:"owner"`
	Models        []string   `json:"models"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"createdAt"`
	RespondedAt   *time.Time `json:"respondedAt"`
}

// Incoming returns the pending and accepted shares of userID (pending first,
// newest first), optionally only the one of channelID.
func (s *Store) Incoming(ctx context.Context, userID uuid.UUID, channelID *uuid.UUID) ([]*IncomingShare, error) {
	args := []any{userID}
	where := ""
	if channelID != nil {
		args = append(args, *channelID)
		where = ` AND c.id = $2`
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.name, c.type, c.status, c.owner_id, u.display_name, sh.status, sh.created_at, sh.responded_at
		FROM channel_shares sh JOIN channels c ON c.id = sh.channel_id JOIN users u ON u.id = c.owner_id
		WHERE sh.user_id = $1 AND sh.status IN ('pending', 'accepted') AND c.scope = 'shared'`+where+`
		ORDER BY CASE WHEN sh.status = 'pending' THEN 0 ELSE 1 END, sh.created_at DESC, c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*IncomingShare{}
	byID := map[uuid.UUID]*IncomingShare{}
	for rows.Next() {
		in := &IncomingShare{Models: []string{}}
		if err := rows.Scan(&in.ChannelID, &in.Name, &in.Type, &in.ChannelStatus, &in.Owner.ID, &in.Owner.DisplayName, &in.Status,
			&in.CreatedAt, &in.RespondedAt); err != nil {
			return nil, err
		}
		out = append(out, in)
		byID[in.ChannelID] = in
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(out))
	for i, in := range out {
		ids[i] = in.ChannelID
	}
	mrows, err := s.pool.Query(ctx, `SELECT channel_id, model FROM channel_models WHERE channel_id = ANY($1) ORDER BY channel_id, position, model`, ids)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var id uuid.UUID
		var m string
		if err := mrows.Scan(&id, &m); err != nil {
			return nil, err
		}
		byID[id].Models = append(byID[id].Models, m)
	}
	return out, mrows.Err()
}

// shareRef identifies the channel of a share for audit entries.
type shareRef struct {
	Name  string
	Owner uuid.UUID
}

// Respond moves the share of (channelID, userID) from status from to status
// to (phase5-api.md §5.3). A share already in status to is left unchanged
// (changed = false). after runs in the transaction when the status changed.
func (s *Store) Respond(ctx context.Context, channelID, userID uuid.UUID, from, to string, after func(db.Tx, shareRef) error) (changed bool, err error) {
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		changed = false
		var st string
		var ref shareRef
		err := tx.QueryRow(ctx, `SELECT sh.status, c.name, c.owner_id FROM channel_shares sh JOIN channels c ON c.id = sh.channel_id
			WHERE sh.channel_id = $1 AND sh.user_id = $2 AND c.scope = 'shared' FOR UPDATE OF sh`, channelID, userID).Scan(&st, &ref.Name, &ref.Owner)
		switch {
		case db.IsNoRows(err) || err == nil && st == ShareDeclined:
			return apperr.NotFound("共享")
		case err != nil:
			return err
		case st == to:
			return nil
		case st != from:
			msg := "该共享已接受，请使用“退出共享”"
			if st == SharePending {
				msg = "该共享尚未接受，请使用“拒绝”"
			}
			return apperr.New(apperr.KindConflict, "share_state_conflict", msg)
		}
		if _, err := tx.Exec(ctx, `UPDATE channel_shares SET status = $3, responded_at = $4 WHERE channel_id = $1 AND user_id = $2`,
			channelID, userID, to, time.Now().UTC()); err != nil {
			return err
		}
		changed = true
		return after(tx, ref)
	})
	return changed, err
}
