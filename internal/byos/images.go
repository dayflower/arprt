package byos

import (
	_ "embed"
	"net/http"
	"strings"

	"context"
	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/imageprocessing"
)

//go:embed setup-logo.png
var setupLogo []byte

func (s *Server) image(w http.ResponseWriter, r *http.Request, d *api.ArprtDevice, endpoint string) {
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
	err = imageprocessing.RuntimeAdmission.Do(ctx, func(ctx context.Context) error {
		b, err := s.Repository.Read(ctx, digest)
		if err != nil {
			reply(w, 404, 404)
			return nil
		}
		pngResponse(w, b)
		return nil
	})
	if err != nil {
		reply(w, 503, 503)
	}
}
