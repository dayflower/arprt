package byos

import (
	"net/http"

	api "github.com/dayflower/arprt/api/v1alpha1"
)

func (s *Server) setup(w http.ResponseWriter, r *http.Request, d *api.ArprtDevice, id string) {
	ctx := r.Context()
	// Secret changes do not increment generation. Re-read external inputs
	// after registration and return only the persisted, current key.
	if err := s.Identity.Check(ctx, d, id); err != nil {
		s.identityError(w, err)
		return
	}
	key, err := s.key(ctx, d)
	if err != nil {
		reply(w, 503, 503)
		return
	}
	jsonResponse(w, 200, struct {
		Status  int    `json:"status"`
		Key     string `json:"api_key"`
		ID      string `json:"friendly_id"`
		Image   string `json:"image_url"`
		Message string `json:"message"`
	}{200, key, d.Name, s.deviceURL(d.Name) + "/setup-logo.png", "Device registered with arprt"})
}
