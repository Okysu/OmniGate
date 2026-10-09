package channel

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/httpx"
)

// User-to-user shares require the recipient's acceptance
// (docs/contracts/phase5-api.md §5): owners invite users through
// sharedWith.users, recipients accept, decline or leave here.

// ShareInvite describes new invitations to a channel (for notifications).
type ShareInvite struct {
	ChannelID   uuid.UUID
	ChannelName string
	Owner       OwnerRef
	Models      []string
	Users       []uuid.UUID
	At          time.Time
}

// invited reports the invitations of the last Create / Update of c.
func (s *Service) invited(ctx context.Context, c *Channel) {
	if len(c.invited) == 0 || s.OnShareInvited == nil {
		return
	}
	models := make([]string, len(c.Models))
	for i, m := range c.Models {
		models[i] = m.Model
	}
	s.OnShareInvited(ctx, ShareInvite{ChannelID: c.ID, ChannelName: c.Name, Owner: c.Owner, Models: models,
		Users: append([]uuid.UUID(nil), c.invited...), At: time.Now().UTC()})
}

// IncomingShares lists the pending and accepted shares of the caller.
func (s *Service) IncomingShares(ctx context.Context, p *authz.Principal) ([]*IncomingShare, error) {
	return s.store.Incoming(ctx, p.UserID, nil)
}

// AcceptShare accepts a pending invitation (idempotent for accepted shares).
func (s *Service) AcceptShare(ctx context.Context, p *authz.Principal, channelID uuid.UUID, meta Meta) (*IncomingShare, error) {
	if err := s.respond(ctx, p, channelID, SharePending, ShareAccepted, "channel.share_accept", meta); err != nil {
		return nil, err
	}
	list, err := s.store.Incoming(ctx, p.UserID, &channelID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apperr.NotFound("共享")
	}
	return list[0], nil
}

// DeclineShare declines a pending invitation.
func (s *Service) DeclineShare(ctx context.Context, p *authz.Principal, channelID uuid.UUID, meta Meta) error {
	return s.respond(ctx, p, channelID, SharePending, ShareDeclined, "channel.share_decline", meta)
}

// LeaveShare ends an accepted share (the recipient opts out).
func (s *Service) LeaveShare(ctx context.Context, p *authz.Principal, channelID uuid.UUID, meta Meta) error {
	return s.respond(ctx, p, channelID, ShareAccepted, ShareDeclined, "channel.share_leave", meta)
}

func (s *Service) respond(ctx context.Context, p *authz.Principal, channelID uuid.UUID, from, to, action string, meta Meta) error {
	changed, err := s.store.Respond(ctx, channelID, p.UserID, from, to, func(tx db.Tx, ref shareRef) error {
		id := channelID.String()
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorID: &p.UserID, ActorName: &p.Name, Action: action, ResourceType: "channel", ResourceID: &id,
			IPPrefix: meta.IPPrefix, RequestID: meta.RequestID, Metadata: map[string]any{"name": ref.Name, "ownerId": ref.Owner},
		})
	})
	if err != nil {
		return err
	}
	if changed {
		s.reg.Invalidate()
	}
	return nil
}

// ShareHandler serves /api/channel-shares (the recipient side).
type ShareHandler struct{ svc *Service }

func (h *Handler) shareRoutes(r chi.Router) {
	sh := &ShareHandler{svc: h.svc}
	r.Route("/channel-shares", func(r chi.Router) {
		r.Use(auth.Require(authz.ChannelsRead))
		r.Get("/", sh.list)
		r.Post("/{channelId}/accept", sh.accept)
		r.Post("/{channelId}/decline", sh.respond((*Service).DeclineShare))
		r.Post("/{channelId}/leave", sh.respond((*Service).LeaveShare))
	})
}

func shareChannelID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "channelId"))
	if err != nil {
		httpx.WriteError(w, r, apperr.NotFound("共享"))
		return uuid.Nil, false
	}
	return id, true
}

func (h *ShareHandler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.IncomingShares(r.Context(), auth.PrincipalFrom(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *ShareHandler) accept(w http.ResponseWriter, r *http.Request) {
	id, ok := shareChannelID(w, r)
	if !ok {
		return
	}
	in, err := h.svc.AcceptShare(r.Context(), auth.PrincipalFrom(r.Context()), id, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, in)
}

func (h *ShareHandler) respond(fn func(*Service, context.Context, *authz.Principal, uuid.UUID, Meta) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := shareChannelID(w, r)
		if !ok {
			return
		}
		if err := fn(h.svc, r.Context(), auth.PrincipalFrom(r.Context()), id, meta(r)); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
