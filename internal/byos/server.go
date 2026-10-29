package byos

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/credentials"
	"github.com/dayflower/arprt/internal/delivery"
	"github.com/dayflower/arprt/internal/deviceidentity"
	"github.com/dayflower/arprt/internal/jobrender"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Server struct {
	Config     Config
	Reader     client.Reader // Uncached API reads for auth, identity and publication gates.
	Client     client.Client
	Repository artifact.Repository
	GET        *delivery.GET
	POST       *delivery.POST
	Jobs       interface {
		Execute(context.Context, jobrender.ExecutionRequest) (artifact.Metadata, error)
	}
	Identity *deviceidentity.Service
	location *time.Location
	base     *url.URL
	gates    [64]chan struct{}
	slots    chan struct{}
	Recorder events.EventRecorder
	demandMu sync.Mutex
	demands  map[string]*demandFlight
	skips    map[string]*skipFlight
}

func NewServer(c Config, reader client.Reader, writer client.Client, repo artifact.Repository) (*Server, error) {
	base, err := c.Base()
	if err != nil {
		return nil, err
	}
	if len(validation.IsDNS1123Label(c.Namespace)) > 0 || c.Namespace == "" {
		return nil, errors.New("invalid BYOS namespace")
	}
	if c.RefreshSeconds == 0 {
		c.RefreshSeconds = 300
	}
	if c.RefreshSeconds < 1 || c.RefreshSeconds > 86400 {
		return nil, errors.New("device refresh must be 1..86400 seconds")
	}
	location, err := overlayLocation(c.OverlayTimeZone)
	if err != nil {
		return nil, err
	}
	get := delivery.NewGET(repo, reader)
	server := &Server{slots: make(chan struct{}, 64), Config: c, Reader: reader, Client: writer, Repository: repo, GET: get, POST: delivery.NewPOST(get), Identity: &deviceidentity.Service{Reader: reader, Writer: writer, Store: repo}, base: base, location: location}
	for i := range server.gates {
		server.gates[i] = make(chan struct{}, 1)
	}
	return server, nil
}

func (s *Server) key(ctx context.Context, d *api.ArprtDevice) (string, error) {
	return credentials.Read(ctx, s.Reader, d)
}
