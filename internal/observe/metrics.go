// Package observe defines metrics with a fixed, bounded label vocabulary.
package observe

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	PlaylistAttempts   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "arprt_playlist_attempts_total", Help: "Skip playlist exploration by bounded outcome."}, []string{"outcome"})
	Renders            = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "arprt_render_duration_seconds", Help: "Completed render attempts, including failures.", Buckets: prometheus.DefBuckets}, []string{"transport", "outcome"})
	HTTPActive         = prometheus.NewGauge(prometheus.GaugeOpts{Name: "arprt_http_render_active", Help: "HTTP renderer execution slots in use."})
	Requests           = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "arprt_requests_total", Help: "BYOS requests by bounded route and HTTP result."}, []string{"route", "result"})
	Rejected           = prometheus.NewCounter(prometheus.CounterOpts{Name: "arprt_request_rejections_total", Help: "Requests rejected by the admission limit."})
	StorageErrors      = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "arprt_storage_errors_total", Help: "Storage faults by fixed operation."}, []string{"operation"})
	OverlayFallbacks   = prometheus.NewCounter(prometheus.CounterOpts{Name: "arprt_overlay_fallbacks_total", Help: "Overlay candidates falling back to their validated base image."})
	CollectionFailures = prometheus.NewCounter(prometheus.CounterOpts{Name: "arprt_collection_failures_total", Help: "Invalid or unavailable completed Job output."})
	StorageBytes       = prometheus.NewGauge(prometheus.GaugeOpts{Name: "arprt_storage_bytes", Help: "Logical bytes in the managed data root, including Job runs."})
	StorageFree        = prometheus.NewGauge(prometheus.GaugeOpts{Name: "arprt_storage_free_bytes", Help: "Filesystem bytes available to the controller."})
	StorageLimit       = prometheus.NewGauge(prometheus.GaugeOpts{Name: "arprt_storage_limit_bytes", Help: "Configured managed storage admission limit."})
	Queue              = prometheus.NewGauge(prometheus.GaugeOpts{Name: "arprt_job_queue_depth", Help: "Due Job pairs waiting for an execution slot."})
	Jobs               = prometheus.NewGauge(prometheus.GaugeOpts{Name: "arprt_jobs_active", Help: "Nonterminal managed Jobs."})
	Devices            = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "arprt_devices", Help: "Devices by last-seen state."}, []string{"state"})
	LastSeenAge        = prometheus.NewGauge(prometheus.GaugeOpts{Name: "arprt_device_last_seen_max_age_seconds", Help: "Maximum age of an observed device; never-seen devices are counted separately."})
	ArtifactAge        = prometheus.NewGauge(prometheus.GaugeOpts{Name: "arprt_artifact_oldest_age_seconds", Help: "Maximum age of latest successful artifacts."})
	Maintenance        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "arprt_maintenance_total", Help: "Maintenance passes by outcome."}, []string{"outcome"})
)

func init() {
	metrics.Registry.MustRegister(PlaylistAttempts, Renders, HTTPActive, Requests, Rejected, StorageErrors, OverlayFallbacks, CollectionFailures, StorageBytes, StorageFree, StorageLimit, Queue, Jobs, Devices, LastSeenAge, ArtifactAge, Maintenance)
}

func Render(transport string, started time.Time, err error) {
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	Renders.WithLabelValues(transport, outcome).Observe(max(0, time.Since(started).Seconds()))
}
