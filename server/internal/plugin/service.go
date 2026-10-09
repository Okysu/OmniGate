package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/platform/db"
	"omnigate/internal/plugin/engine"
)

type VersionSummary struct {
	ID          uuid.UUID  `json:"id"`
	Version     string     `json:"version"`
	Approval    string     `json:"approval"`
	PublishedAt time.Time  `json:"publishedAt"`
	PublishedBy *uuid.UUID `json:"publishedBy"`
	ContentHash string     `json:"contentHash"`
	RiskCount   int        `json:"riskCount"`
}

type Plugin struct {
	ID          uuid.UUID       `json:"id"`
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Author      string          `json:"author"`
	Homepage    string          `json:"homepage"`
	Source      string          `json:"source"`
	Status      string          `json:"status"`
	Version     int             `json:"version"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	Latest      *VersionSummary `json:"latest"`  // latest approved
	Pending     *VersionSummary `json:"pending"` // newest pending approval
	Channels    int             `json:"channels"`
	HasDraft    bool            `json:"hasDraft"`
	Extends     string          `json:"extends"`
	// Protocol, Kind and Meters describe the latest approved version
	// (phase9-api.md §2, §3): protocol "custom" or "", kind defaults to
	// ["channel"], meters lists billing meters (empty unless kind has billing).
	Protocol string           `json:"protocol"`
	Kind     []string         `json:"kind"`
	Meters   []MeterInfo      `json:"meters"`
	Versions []VersionSummary `json:"versions,omitempty"`
}

// MeterInfo is a billing meter of a plugin version.
type MeterInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Unit  string `json:"unit,omitempty"`
}

// summarize fills the latest-approved-version fields of p from manifest.
func (p *Plugin) summarize(m *Manifest) {
	p.Extends, p.Protocol, p.Kind, p.Meters = m.Extends, m.Protocol, m.Kind, []MeterInfo{}
	if len(p.Kind) == 0 {
		p.Kind = []string{KindChannel}
	}
	if m.Billing != nil && m.HasKind(KindBilling) {
		for _, name := range sortedKeys(m.Billing.Meters) {
			d := m.Billing.Meters[name]
			p.Meters = append(p.Meters, MeterInfo{Name: name, Label: d.Label, Unit: d.Unit})
		}
	}
}

type Version struct {
	VersionSummary
	PluginID       uuid.UUID         `json:"pluginId"`
	Manifest       *Manifest         `json:"manifest"`
	Files          map[string]string `json:"files"`
	Risk           []RiskFinding     `json:"risk"`
	ApprovedBy     *uuid.UUID        `json:"approvedBy"`
	ApprovedAt     *time.Time        `json:"approvedAt"`
	ApprovalNote   *string           `json:"approvalNote"`
	PermissionDiff PermissionDiff    `json:"permissionDiff"`
	bundle         string
}

type Draft struct {
	Files         map[string]string `json:"files"`
	BaseVersionID *uuid.UUID        `json:"baseVersionId"`
	UpdatedAt     time.Time         `json:"updatedAt"`
	Version       int               `json:"version"`
}

type BuildResult struct {
	OK          bool          `json:"ok"`
	Manifest    *Manifest     `json:"manifest"`
	Diagnostics []Diagnostic  `json:"diagnostics"`
	Risk        []RiskFinding `json:"risk"`
	BundleBytes int           `json:"bundleBytes"`
	bundle      string
	hash        string
}

// Meta carries request info for audit entries.
type Meta struct {
	IPPrefix  string
	RequestID string
}

type Service struct {
	pool   *db.DB
	audit  *audit.Recorder
	engine *engine.Engine
	log    *slog.Logger

	// OnChange is called after plugin status changes (registry reload).
	OnChange func()
	// OnPendingApproval is called after a published version awaits approval.
	OnPendingApproval func(ctx context.Context, v PendingVersion)

	mu       sync.Mutex
	loaded   map[uuid.UUID]*Loaded // immutable versions
	status   map[uuid.UUID]string  // plugin id -> status
	hashOwns map[string]uuid.UUID  // content hash -> plugin id (for violations)
	viol     map[uuid.UUID][]time.Time
}

// PendingVersion is a published plugin version waiting for approval.
type PendingVersion struct {
	PluginID  uuid.UUID
	VersionID uuid.UUID
	Key       string
	Name      string
	Version   string
	Publisher string
}

func NewService(pool *db.DB, rec *audit.Recorder, log *slog.Logger, cfg engine.Config) *Service {
	s := &Service{pool: pool, audit: rec, log: log, loaded: map[uuid.UUID]*Loaded{}, status: map[uuid.UUID]string{},
		hashOwns: map[string]uuid.UUID{}, viol: map[uuid.UUID][]time.Time{}}
	cfg.OnViolation = s.onViolation
	s.engine = engine.New(cfg, log)
	return s
}

// Engine exposes the sandbox (watchdog loop is started by the app).
func (s *Service) Engine() *engine.Engine { return s.engine }

// ---- build ----

// Build compiles and validates a package (no side effects).
func Build(files map[string]string, builtin bool) *BuildResult {
	r := &BuildResult{Diagnostics: []Diagnostic{}, Risk: []RiskFinding{}}
	r.Diagnostics = append(r.Diagnostics, CheckFiles(files)...)
	if raw, ok := files["manifest.json"]; ok {
		m, ds := ParseManifest([]byte(raw), files, builtin)
		r.Manifest = m
		r.Diagnostics = append(r.Diagnostics, ds...)
	}
	if r.Manifest != nil && !builtin && !hasErrors(r.Diagnostics) {
		bundle, ds := Compile(files, r.Manifest.Entry)
		r.Diagnostics = append(r.Diagnostics, ds...)
		r.bundle, r.BundleBytes = bundle, len(bundle)
	}
	if r.Manifest != nil {
		r.Risk = ScanRisks(files, r.Manifest.Permissions.Network)
	}
	r.hash = ContentHash(files)
	r.OK = !hasErrors(r.Diagnostics) && (builtin || r.bundle != "")
	return r
}

func hasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

// checkExports verifies that declared capabilities and hooks are implemented.
func (s *Service) checkExports(r *BuildResult) {
	if !r.OK || r.bundle == "" {
		return
	}
	p, err := s.engine.Program("check-"+r.hash, r.bundle)
	if err != nil {
		r.Diagnostics = append(r.Diagnostics, Diagnostic{File: r.Manifest.Entry, Severity: "error", Message: err.Error()})
		r.OK = false
		return
	}
	defer s.engine.Drop("check-" + r.hash)
	for name := range r.Manifest.Capabilities {
		if !p.Has("capabilities", name) {
			r.Diagnostics = append(r.Diagnostics, Diagnostic{File: r.Manifest.Entry, Severity: "error",
				Message: fmt.Sprintf("manifest 声明了能力 %s，但插件没有在 capabilities 中实现", name)})
		}
	}
	for _, h := range r.Manifest.Hooks {
		if !p.Has(h) {
			r.Diagnostics = append(r.Diagnostics, Diagnostic{File: r.Manifest.Entry, Severity: "error",
				Message: fmt.Sprintf("manifest 声明了 Hook %s，但插件没有实现", h)})
		}
	}
	if r.Manifest.Billing != nil {
		for _, name := range sortedKeys(r.Manifest.Billing.Meters) {
			if !p.Has("billing", "meters", name, "computeUnits") {
				r.Diagnostics = append(r.Diagnostics, Diagnostic{File: r.Manifest.Entry, Severity: "error",
					Message: fmt.Sprintf("manifest 声明了计量 %s，但插件没有实现 billing.meters.%s.computeUnits", name, name)})
			}
		}
	}
	r.OK = !hasErrors(r.Diagnostics)
}

// ---- queries ----

const pluginCols = `p.id, p.plugin_key, p.name, p.description, p.author, p.homepage, p.source, p.status, p.version, p.created_at, p.updated_at`

func scanPlugin(row db.Row) (*Plugin, error) {
	var p Plugin
	err := row.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.Author, &p.Homepage, &p.Source, &p.Status, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	return &p, err
}

func (s *Service) decorate(ctx context.Context, list []*Plugin) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(list))
	byID := map[uuid.UUID]*Plugin{}
	for i, p := range list {
		ids[i], byID[p.ID] = p.ID, p
		p.Kind, p.Meters = []string{KindChannel}, []MeterInfo{}
	}
	rows, err := s.pool.Query(ctx, `SELECT id, plugin_id, version, approval, published_at, published_by, content_hash,
		jsonb_array_length(risk), manifest->>'extends', manifest->>'protocol', manifest->'kind', manifest->'billing'
		FROM plugin_versions WHERE plugin_id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v VersionSummary
		var pid uuid.UUID
		var ext, proto, kind, billing *string
		if err := rows.Scan(&v.ID, &pid, &v.Version, &v.Approval, &v.PublishedAt, &v.PublishedBy, &v.ContentHash, &v.RiskCount,
			&ext, &proto, &kind, &billing); err != nil {
			rows.Close()
			return err
		}
		p := byID[pid]
		switch v.Approval {
		case "approved":
			if p.Latest == nil || CompareSemver(v.Version, p.Latest.Version) > 0 {
				vv := v
				p.Latest = &vv
				var m Manifest
				if ext != nil {
					m.Extends = *ext
				}
				if proto != nil {
					m.Protocol = *proto
				}
				if kind != nil {
					_ = json.Unmarshal([]byte(*kind), &m.Kind)
				}
				if billing != nil {
					_ = json.Unmarshal([]byte(*billing), &m.Billing)
				}
				p.summarize(&m)
			}
		case "pending":
			if p.Pending == nil || CompareSemver(v.Version, p.Pending.Version) > 0 {
				vv := v
				p.Pending = &vv
			}
		}
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT v.plugin_id, count(c.id) FROM channels c JOIN plugin_versions v ON v.id = c.plugin_version_id
		WHERE v.plugin_id = ANY($1) GROUP BY v.plugin_id`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var pid uuid.UUID
		var n int
		if err := rows.Scan(&pid, &n); err != nil {
			rows.Close()
			return err
		}
		byID[pid].Channels = n
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT plugin_id FROM plugin_drafts WHERE plugin_id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid uuid.UUID
		if err := rows.Scan(&pid); err != nil {
			return err
		}
		byID[pid].HasDraft = true
	}
	return rows.Err()
}

func (s *Service) List(ctx context.Context) ([]*Plugin, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+pluginCols+` FROM plugins p ORDER BY p.source = 'builtin' DESC, p.name`)
	if err != nil {
		return nil, err
	}
	list := []*Plugin{}
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, p)
	}
	rows.Close()
	return list, s.decorate(ctx, list)
}

func (s *Service) getPlugin(ctx context.Context, q db.Querier, id uuid.UUID, lock bool) (*Plugin, error) {
	sql := `SELECT ` + pluginCols + ` FROM plugins p WHERE p.id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	p, err := scanPlugin(q.QueryRow(ctx, sql, id))
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("插件")
	}
	return p, err
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Plugin, error) {
	p, err := s.getPlugin(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	if err := s.decorate(ctx, []*Plugin{p}); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, version, approval, published_at, published_by, content_hash, jsonb_array_length(risk)
		FROM plugin_versions WHERE plugin_id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p.Versions = []VersionSummary{}
	for rows.Next() {
		var v VersionSummary
		if err := rows.Scan(&v.ID, &v.Version, &v.Approval, &v.PublishedAt, &v.PublishedBy, &v.ContentHash, &v.RiskCount); err != nil {
			return nil, err
		}
		p.Versions = append(p.Versions, v)
	}
	sort.Slice(p.Versions, func(i, j int) bool { return CompareSemver(p.Versions[i].Version, p.Versions[j].Version) > 0 })
	return p, rows.Err()
}

func (s *Service) loadVersion(ctx context.Context, q db.Querier, pluginID, versionID uuid.UUID) (*Version, error) {
	var v Version
	var manifest, files, risk []byte
	var bundle *string
	err := q.QueryRow(ctx, `SELECT id, plugin_id, version, approval, published_at, published_by, content_hash, manifest, files, risk,
		bundle, approved_by, approved_at, approval_note FROM plugin_versions WHERE id = $1 AND plugin_id = $2`, versionID, pluginID).
		Scan(&v.ID, &v.PluginID, &v.Version, &v.Approval, &v.PublishedAt, &v.PublishedBy, &v.ContentHash, &manifest, &files, &risk,
			&bundle, &v.ApprovedBy, &v.ApprovedAt, &v.ApprovalNote)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("插件版本")
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(manifest, &v.Manifest)
	_ = json.Unmarshal(files, &v.Files)
	_ = json.Unmarshal(risk, &v.Risk)
	v.RiskCount = len(v.Risk)
	if bundle != nil {
		v.bundle = *bundle
	}
	return &v, nil
}

// latestApproved returns the newest approved version's manifest (nil if none).
func (s *Service) latestApproved(ctx context.Context, q db.Querier, pluginID uuid.UUID, excluding uuid.UUID) (*Manifest, error) {
	rows, err := q.Query(ctx, `SELECT id, version, manifest FROM plugin_versions WHERE plugin_id = $1 AND approval = 'approved' AND id <> $2`, pluginID, excluding)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var best *Manifest
	for rows.Next() {
		var id uuid.UUID
		var ver string
		var raw []byte
		if err := rows.Scan(&id, &ver, &raw); err != nil {
			return nil, err
		}
		var m Manifest
		if json.Unmarshal(raw, &m) == nil && (best == nil || CompareSemver(m.Version, best.Version) > 0) {
			mm := m
			best = &mm
		}
	}
	return best, rows.Err()
}

func (s *Service) GetVersion(ctx context.Context, pluginID, versionID uuid.UUID) (*Version, error) {
	v, err := s.loadVersion(ctx, s.pool, pluginID, versionID)
	if err != nil {
		return nil, err
	}
	// Diff against the newest approved version older than this one.
	prev, err := s.approvedBefore(ctx, pluginID, v.Version)
	if err != nil {
		return nil, err
	}
	if v.Manifest != nil {
		v.PermissionDiff = DiffPermissions(prev, v.Manifest.Permissions)
	}
	return v, nil
}

func (s *Service) approvedBefore(ctx context.Context, pluginID uuid.UUID, version string) (*Permissions, error) {
	rows, err := s.pool.Query(ctx, `SELECT manifest FROM plugin_versions WHERE plugin_id = $1 AND approval = 'approved'`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var best *Manifest
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m Manifest
		if json.Unmarshal(raw, &m) == nil && CompareSemver(m.Version, version) < 0 && (best == nil || CompareSemver(m.Version, best.Version) > 0) {
			mm := m
			best = &mm
		}
	}
	if best == nil {
		return nil, rows.Err()
	}
	return &best.Permissions, rows.Err()
}

// ---- mutations ----

func (s *Service) record(ctx context.Context, q db.Querier, p *authz.Principal, meta Meta, action, pluginID string, md map[string]any) error {
	e := audit.Entry{Action: action, ResourceType: "plugin", ResourceID: &pluginID, IPPrefix: meta.IPPrefix, RequestID: meta.RequestID, Metadata: md}
	if p != nil {
		e.ActorID, e.ActorName = &p.UserID, &p.Name
	}
	return s.audit.Record(ctx, q, e)
}

// CreateFromTemplate creates an editor plugin with a draft.
func (s *Service) CreateFromTemplate(ctx context.Context, p *authz.Principal, key, name, template string, meta Meta) (*Plugin, error) {
	if !pluginIDRe.MatchString(key) || strings.HasPrefix(key, "builtin.") || len(key) > 64 {
		return nil, apperr.Validation("参数校验失败", map[string]any{"id": "必须形如 vendor.name，且不能使用 builtin. 前缀"})
	}
	if n := len([]rune(strings.TrimSpace(name))); n == 0 || n > 64 {
		return nil, apperr.Validation("参数校验失败", map[string]any{"name": "长度应为 1–64 个字符"})
	}
	files, ok := templateFiles(template, key, strings.TrimSpace(name))
	if !ok {
		return nil, apperr.Validation("参数校验失败", map[string]any{"template": "未知模板"})
	}
	return s.createWithDraft(ctx, p, key, strings.TrimSpace(name), "editor", files, meta, "plugin.create")
}

func (s *Service) createWithDraft(ctx context.Context, p *authz.Principal, key, name, source string, files map[string]string, meta Meta, action string) (*Plugin, error) {
	id := uuid.Must(uuid.NewV7())
	filesJSON, _ := json.Marshal(files)
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO plugins (id, plugin_key, name, source, created_by) VALUES ($1, $2, $3, $4, $5)`,
			id, key, name, source, p.UserID); err != nil {
			if db.IsUniqueViolation(err) {
				return apperr.New(apperr.KindConflict, "plugin_exists", "插件 ID 已存在")
			}
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plugin_drafts (plugin_id, files, updated_by) VALUES ($1, $2, $3)`, id, filesJSON, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, meta, action, id.String(), map[string]any{"key": key})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Import accepts a ZIP package. An existing (non-builtin) plugin with the same
// id gets its draft replaced; otherwise a new plugin is created.
func (s *Service) Import(ctx context.Context, p *authz.Principal, data []byte, meta Meta) (*Plugin, *BuildResult, error) {
	files, err := ReadZip(data)
	if err != nil {
		return nil, nil, apperr.Validation(err.Error(), nil)
	}
	raw, ok := files["manifest.json"]
	if !ok {
		return nil, nil, apperr.Validation("压缩包中缺少 manifest.json", nil)
	}
	var head struct{ ID, Name string }
	if err := json.Unmarshal([]byte(raw), &head); err != nil || !pluginIDRe.MatchString(head.ID) || strings.HasPrefix(head.ID, "builtin.") {
		return nil, nil, apperr.Validation("manifest.json 中的 id 无效", nil)
	}
	build := Build(files, false)
	var existing uuid.UUID
	err = s.pool.QueryRow(ctx, `SELECT id FROM plugins WHERE plugin_key = $1 AND source <> 'builtin'`, head.ID).Scan(&existing)
	switch {
	case db.IsNoRows(err):
		name := head.Name
		if name == "" {
			name = head.ID
		}
		pl, err := s.createWithDraft(ctx, p, head.ID, name, "upload", files, meta, "plugin.import")
		return pl, build, err
	case err != nil:
		return nil, nil, err
	}
	if _, err := s.saveDraft(ctx, p, existing, files, nil, meta); err != nil {
		return nil, nil, err
	}
	_ = s.record(ctx, nil, p, meta, "plugin.import", existing.String(), map[string]any{"key": head.ID, "into": "draft"})
	pl, err := s.Get(ctx, existing)
	return pl, build, err
}

func (s *Service) GetDraft(ctx context.Context, pluginID uuid.UUID) (*Draft, error) {
	var d Draft
	var files []byte
	err := s.pool.QueryRow(ctx, `SELECT files, base_version_id, updated_at, version FROM plugin_drafts WHERE plugin_id = $1`, pluginID).
		Scan(&files, &d.BaseVersionID, &d.UpdatedAt, &d.Version)
	if db.IsNoRows(err) {
		// No draft yet: start one from the latest approved version.
		pl, err := s.Get(ctx, pluginID)
		if err != nil {
			return nil, err
		}
		if pl.Source == "builtin" {
			return nil, apperr.New(apperr.KindConflict, "plugin_builtin", "内置插件不能编辑")
		}
		base := pl.Latest
		for i := range pl.Versions { // newest first; covers plugins whose only versions are pending
			if base == nil || CompareSemver(pl.Versions[i].Version, base.Version) > 0 {
				base = &pl.Versions[i]
			}
		}
		if base == nil {
			return nil, apperr.NotFound("草稿")
		}
		v, err := s.loadVersion(ctx, s.pool, pluginID, base.ID)
		if err != nil {
			return nil, err
		}
		return &Draft{Files: v.Files, BaseVersionID: &v.ID, Version: 0}, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(files, &d.Files)
	return &d, nil
}

// SaveDraft replaces the draft files (version 0 creates the draft).
func (s *Service) SaveDraft(ctx context.Context, p *authz.Principal, pluginID uuid.UUID, files map[string]string, version int, meta Meta) (*Draft, error) {
	return s.saveDraft(ctx, p, pluginID, files, &version, meta)
}

func (s *Service) saveDraft(ctx context.Context, p *authz.Principal, pluginID uuid.UUID, files map[string]string, version *int, meta Meta) (*Draft, error) {
	if ds := CheckFiles(files); hasErrors(ds) {
		return nil, apperr.Validation(ds[0].Message, map[string]any{"files": ds})
	}
	filesJSON, _ := json.Marshal(files)
	var d *Draft
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		pl, err := s.getPlugin(ctx, tx, pluginID, true)
		if err != nil {
			return err
		}
		if pl.Source == "builtin" {
			return apperr.New(apperr.KindConflict, "plugin_builtin", "内置插件不能编辑")
		}
		var cur int
		err = tx.QueryRow(ctx, `SELECT version FROM plugin_drafts WHERE plugin_id = $1 FOR UPDATE`, pluginID).Scan(&cur)
		switch {
		case db.IsNoRows(err):
			if version != nil && *version != 0 {
				return apperr.VersionConflict()
			}
			if _, err := tx.Exec(ctx, `INSERT INTO plugin_drafts (plugin_id, files, updated_by) VALUES ($1, $2, $3)`, pluginID, filesJSON, p.UserID); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if version != nil && *version != cur {
				return apperr.VersionConflict()
			}
			if _, err := tx.Exec(ctx, `UPDATE plugin_drafts SET files = $2, updated_by = $3, updated_at = now(), version = version + 1 WHERE plugin_id = $1`,
				pluginID, filesJSON, p.UserID); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, meta, "plugin.draft_update", pluginID.String(), map[string]any{"files": len(files)})
	})
	if err != nil {
		return nil, err
	}
	if d, err = s.GetDraft(ctx, pluginID); err != nil {
		return nil, err
	}
	return d, nil
}

// BuildDraft compiles the current draft.
func (s *Service) BuildDraft(ctx context.Context, pluginID uuid.UUID) (*BuildResult, error) {
	d, err := s.GetDraft(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	r := Build(d.Files, false)
	s.checkExports(r)
	return r, nil
}

// Publish turns the draft into an immutable version.
func (s *Service) Publish(ctx context.Context, p *authz.Principal, pluginID uuid.UUID, meta Meta) (*Version, error) {
	d, err := s.GetDraft(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	r := Build(d.Files, false)
	s.checkExports(r)
	if !r.OK {
		return nil, &apperr.Error{Kind: apperr.KindValidation, Code: "plugin_build_failed", Message: "插件编译或校验未通过", Details: map[string]any{"diagnostics": r.Diagnostics}}
	}
	var vid uuid.UUID
	var pending *PendingVersion
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		pending = nil
		pl, err := s.getPlugin(ctx, tx, pluginID, true)
		if err != nil {
			return err
		}
		if r.Manifest.ID != pl.Key {
			return apperr.Validation("manifest.id 必须与插件 ID 一致："+pl.Key, nil)
		}
		var maxVer string
		rows, err := tx.Query(ctx, `SELECT version FROM plugin_versions WHERE plugin_id = $1`, pluginID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			if maxVer == "" || CompareSemver(v, maxVer) > 0 {
				maxVer = v
			}
		}
		rows.Close()
		if maxVer != "" && CompareSemver(r.Manifest.Version, maxVer) <= 0 {
			return apperr.New(apperr.KindConflict, "plugin_version_exists", fmt.Sprintf("版本号必须大于已发布的 %s", maxVer))
		}
		prevM, err := s.latestApproved(ctx, tx, pluginID, uuid.Nil)
		if err != nil {
			return err
		}
		var prev *Permissions
		if prevM != nil {
			prev = &prevM.Permissions
		}
		diff := DiffPermissions(prev, r.Manifest.Permissions)
		approval, note := "pending", (*string)(nil)
		var approvedBy *uuid.UUID
		var approvedAt *time.Time
		// The first custom-protocol version always needs approval, even when
		// the permissions match an approved inheriting version (phase9-api.md §2).
		firstCustom := r.Manifest.CustomProtocol() && (prevM == nil || !prevM.CustomProtocol())
		if prev != nil && diff.Empty() && !firstCustom {
			approval = "approved"
			n := "权限与已批准的 " + prevM.Version + " 相同，自动批准"
			now := time.Now().UTC()
			note, approvedBy, approvedAt = &n, &p.UserID, &now
		}
		vid = uuid.Must(uuid.NewV7())
		if approval == "pending" {
			pending = &PendingVersion{PluginID: pluginID, VersionID: vid, Key: pl.Key, Name: r.Manifest.Name, Version: r.Manifest.Version, Publisher: p.Name}
		}
		manifest, _ := json.Marshal(r.Manifest)
		files, _ := json.Marshal(d.Files)
		risk, _ := json.Marshal(r.Risk)
		if _, err := tx.Exec(ctx, `INSERT INTO plugin_versions (id, plugin_id, version, manifest, files, bundle, content_hash, risk,
			approval, approved_by, approved_at, approval_note, published_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			vid, pluginID, r.Manifest.Version, manifest, files, r.bundle, r.hash, risk, approval, approvedBy, approvedAt, note, p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE plugins SET name = $2, description = $3, author = $4, homepage = $5, updated_at = now() WHERE id = $1`,
			pluginID, r.Manifest.Name, r.Manifest.Description, r.Manifest.Author, r.Manifest.Homepage); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM plugin_drafts WHERE plugin_id = $1`, pluginID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, meta, "plugin.publish", pluginID.String(), map[string]any{
			"version": r.Manifest.Version, "approval": approval, "permissionsAdded": diff.Added, "risk": len(r.Risk), "contentHash": r.hash})
	})
	if err != nil {
		return nil, err
	}
	if pending != nil && s.OnPendingApproval != nil {
		s.OnPendingApproval(ctx, *pending)
	}
	return s.GetVersion(ctx, pluginID, vid)
}

// Approve records a decision on a pending version.
func (s *Service) Approve(ctx context.Context, p *authz.Principal, pluginID, versionID uuid.UUID, approve bool, note string, meta Meta) (*Version, error) {
	decision, action := "rejected", "plugin.reject"
	if approve {
		decision, action = "approved", "plugin.approve"
	}
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE plugin_versions SET approval = $3, approved_by = $4, approved_at = now(), approval_note = $5
			WHERE id = $1 AND plugin_id = $2 AND approval = 'pending'`, versionID, pluginID, decision, p.UserID, nullable(note))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.KindConflict, "not_pending", "该版本不处于待审批状态")
		}
		return s.record(ctx, tx, p, meta, action, pluginID.String(), map[string]any{"versionId": versionID.String(), "note": note})
	})
	if err != nil {
		return nil, err
	}
	return s.GetVersion(ctx, pluginID, versionID)
}

func (s *Service) UpdateStatus(ctx context.Context, p *authz.Principal, pluginID uuid.UUID, status string, version int, meta Meta) (*Plugin, error) {
	if status != "enabled" && status != "disabled" {
		return nil, apperr.Validation("参数校验失败", map[string]any{"status": "只能是 enabled 或 disabled"})
	}
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		pl, err := s.getPlugin(ctx, tx, pluginID, true)
		if err != nil {
			return err
		}
		if pl.Source == "builtin" && status == "disabled" {
			return apperr.New(apperr.KindConflict, "plugin_builtin", "内置插件不能停用（可停用具体渠道）")
		}
		tag, err := tx.Exec(ctx, `UPDATE plugins SET status = $2, version = version + 1, updated_at = now() WHERE id = $1 AND version = $3`, pluginID, status, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := s.getPlugin(ctx, tx, pluginID, false); err != nil {
				return err
			}
			return apperr.VersionConflict()
		}
		return s.record(ctx, tx, p, meta, "plugin.update", pluginID.String(), map[string]any{"status": status})
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.status[pluginID] = status
	s.mu.Unlock()
	if s.OnChange != nil {
		s.OnChange()
	}
	return s.Get(ctx, pluginID)
}

// Delete removes an uploaded/editor plugin that no channel uses (versions,
// drafts and plugin storage cascade).
func (s *Service) Delete(ctx context.Context, p *authz.Principal, pluginID uuid.UUID, meta Meta) error {
	var versionIDs []uuid.UUID
	err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
		pl, err := s.getPlugin(ctx, tx, pluginID, true)
		if err != nil {
			return err
		}
		if pl.Source == "builtin" || pl.Source == "bundled" {
			return apperr.New(apperr.KindConflict, "plugin_builtin", "内置和随附插件不能删除（可以停用）")
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM channels c JOIN plugin_versions v ON v.id = c.plugin_version_id WHERE v.plugin_id = $1`, pluginID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &apperr.Error{Kind: apperr.KindConflict, Code: "plugin_in_use", Message: fmt.Sprintf("仍有 %d 个渠道使用该插件，请先删除这些渠道或切换到其他版本", n), Details: map[string]any{"channels": n}}
		}
		rows, err := tx.Query(ctx, `SELECT id FROM plugin_versions WHERE plugin_id = $1`, pluginID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			versionIDs = append(versionIDs, id)
		}
		rows.Close()
		if _, err := tx.Exec(ctx, `DELETE FROM plugins WHERE id = $1`, pluginID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, meta, "plugin.delete", pluginID.String(), map[string]any{"key": pl.Key, "versions": len(versionIDs)})
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, id := range versionIDs {
		delete(s.loaded, id)
	}
	delete(s.status, pluginID)
	s.mu.Unlock()
	return nil
}

// Export builds a ZIP of a version's files.
func (s *Service) Export(ctx context.Context, pluginID, versionID uuid.UUID) (string, []byte, error) {
	v, err := s.loadVersion(ctx, s.pool, pluginID, versionID)
	if err != nil {
		return "", nil, err
	}
	if len(v.Files) == 0 {
		return "", nil, apperr.New(apperr.KindConflict, "plugin_builtin", "内置插件没有可导出的文件")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	keys := make([]string, 0, len(v.Files))
	for k := range v.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: k, Method: zip.Deflate, Modified: v.PublishedAt})
		if err != nil {
			return "", nil, err
		}
		if _, err := io.WriteString(w, v.Files[k]); err != nil {
			return "", nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return "", nil, err
	}
	name := v.Manifest.ID + "-" + v.Version + ".zip"
	return name, buf.Bytes(), nil
}

// ReadZip extracts a plugin package with size limits (zip-bomb safe).
func ReadZip(data []byte) (map[string]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("不是合法的 ZIP 文件")
	}
	files := map[string]string{}
	total := 0
	// Allow packages wrapped in a single top-level directory.
	prefix := ""
	for _, f := range zr.File {
		if path.Base(f.Name) == "manifest.json" && strings.Count(strings.TrimSuffix(f.Name, "/"), "/") <= 1 {
			prefix = strings.TrimSuffix(f.Name, "manifest.json")
			break
		}
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if len(files) >= MaxFiles {
			return nil, fmt.Errorf("文件数超过 %d", MaxFiles)
		}
		name := strings.TrimPrefix(f.Name, prefix)
		if !pathRe.MatchString(name) || strings.Contains(name, "..") {
			return nil, fmt.Errorf("文件路径不合法：%s", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, MaxFileBytes+1))
		rc.Close()
		if err != nil {
			return nil, err
		}
		if len(b) > MaxFileBytes {
			return nil, fmt.Errorf("文件 %s 超过 256 KiB", name)
		}
		total += len(b)
		if total > MaxTotalBytes {
			return nil, errors.New("解压后总大小超过 1 MiB")
		}
		files[name] = string(b)
	}
	return files, nil
}

// ---- violations / auto-disable ----

func (s *Service) onViolation(programKey, kind string) {
	s.mu.Lock()
	pid, ok := s.hashOwns[programKey]
	if !ok {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	var recent []time.Time
	for _, t := range append(s.viol[pid], now) {
		if now.Sub(t) < 5*time.Minute {
			recent = append(recent, t)
		}
	}
	s.viol[pid] = recent
	trip := len(recent) >= 3 && s.status[pid] != "disabled"
	if trip {
		s.status[pid] = "disabled"
		s.viol[pid] = nil
	}
	s.mu.Unlock()
	s.log.Warn("plugin resource violation", "plugin_id", pid, "kind", kind, "recent", len(recent))
	if !trip {
		return
	}
	go func() {
		ctx := context.Background()
		err := db.InTx(ctx, s.pool, func(tx db.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE plugins SET status = 'disabled', version = version + 1, updated_at = now() WHERE id = $1`, pid); err != nil {
				return err
			}
			return s.record(ctx, tx, nil, Meta{}, "plugin.auto_disabled", pid.String(), map[string]any{"reason": "5 分钟内 3 次资源违规", "lastKind": kind})
		})
		if err != nil {
			s.log.Error("auto-disable plugin failed", "plugin_id", pid, "err", err)
		}
		if s.OnChange != nil {
			s.OnChange()
		}
	}()
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
