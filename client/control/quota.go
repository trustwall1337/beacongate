package control

import (
	"net/http"

	"github.com/trustwall1337/beacongate/engine/transport/appsscript"
)

// QuotaResponse is the payload of GET /api/quota. When the active
// transport is appsscript, .AppsScript is populated; for other
// transports it's nil and .Note explains why.
type QuotaResponse struct {
	TransportType string            `json:"transport_type"`
	Note          string            `json:"note,omitempty"`
	AppsScript    *appsscript.Stats `json:"appsscript,omitempty"`
}

func (a *API) handleQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := a.rt.Config()
	resp := QuotaResponse{TransportType: cfg.Transport.Type}

	if c, ok := a.rt.Transport().(*appsscript.Client); ok {
		s := c.Stats()
		resp.AppsScript = &s
	} else {
		resp.Note = "quota tracking is only available for the appsscript transport"
	}
	writeJSON(w, http.StatusOK, resp)
}
