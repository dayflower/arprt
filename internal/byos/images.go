package byos

import (
	_ "embed"
	"net/http"
	"strings"

	api "github.com/dayflower/arprt/api/v1alpha1"
)

//go:embed setup-logo.png
var setupLogo []byte

func (s *Server) image(w http.ResponseWriter, r *http.Request, d *api.TrmnlDevice, endpoint string) {
	ctx := r.Context()
	digest := strings.TrimSuffix(strings.TrimPrefix(endpoint, "images/"), ".png")
	allowed, err := s.Repository.Authorized(ctx, string(d.UID), digest)
	if err != nil {
		reply(w, 503, 503)
		return
	}
	if !allowed {
		reply(w, 404, 404)
		return
	}
	b, err := s.Repository.Read(ctx, digest)
	if err != nil {
		reply(w, 404, 404)
		return
	}
	pngResponse(w, b)
}
