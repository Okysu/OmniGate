package billing

import (
	"net/http"

	"github.com/google/uuid"

	"omnigate/internal/platform/httpx"
)

func (h *Handler) lottery(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.Lottery(r.Context(), actor(r).ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) getLotteryConfig(w http.ResponseWriter, r *http.Request) {
	v, err := lotteryConfig(r.Context(), h.svc.pool)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) putLotteryConfig(w http.ResponseWriter, r *http.Request) {
	var in LotteryConfig
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.svc.SetLottery(r.Context(), actor(r), in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) drawLottery(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Count     int       `json:"count"`
		RequestID uuid.UUID `json:"requestId"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.svc.DrawLottery(r.Context(), actor(r).ID, in.RequestID, in.Count)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}
