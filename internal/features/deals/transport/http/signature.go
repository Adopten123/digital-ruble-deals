package dealshttp

import (
	"net/http"

	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
)

func (h *Handler) sign(w http.ResponseWriter, r *http.Request) {
	a, key, ok := h.command(w, r)
	if !ok {
		return
	}
	var in service.SignInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	out, err := h.service.Sign(r.Context(), a, r.PathValue("id"), key, in)
	result(w, r, out, err)
}
