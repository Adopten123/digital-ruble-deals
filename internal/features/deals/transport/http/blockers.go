package dealshttp

import (
	"net/http"

	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
)

func (h *Handler) dispute(w http.ResponseWriter, r *http.Request) {
	a, key, ok := h.command(w, r)
	if !ok {
		return
	}
	var in service.ReasonInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	out, err := h.service.Dispute(r.Context(), a, r.PathValue("id"), key, in)
	result(w, r, out, err)
}
func (h *Handler) requestCancellation(w http.ResponseWriter, r *http.Request) {
	a, key, ok := h.command(w, r)
	if !ok {
		return
	}
	var in service.ReasonInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	out, err := h.service.RequestCancellation(r.Context(), a, r.PathValue("id"), key, in)
	result(w, r, out, err)
}
func (h *Handler) acceptCancellation(w http.ResponseWriter, r *http.Request) {
	a, key, ok := h.command(w, r)
	if !ok {
		return
	}
	var in service.CancelAcceptInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	out, err := h.service.AcceptCancellation(r.Context(), a, r.PathValue("id"), r.PathValue("request_id"), key, in)
	result(w, r, out, err)
}
func (h *Handler) rejectCancellation(w http.ResponseWriter, r *http.Request) {
	a, key, ok := h.command(w, r)
	if !ok {
		return
	}
	var in service.ReasonInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	out, err := h.service.RejectCancellation(r.Context(), a, r.PathValue("id"), r.PathValue("request_id"), key, in)
	result(w, r, out, err)
}
