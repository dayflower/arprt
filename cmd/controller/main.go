package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/byos"
	"github.com/dayflower/arprt/internal/controller"
	"github.com/dayflower/arprt/internal/jobrender"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var namespace, root, probes, listen, publicURL string
	var enableSetup, strips bool
	var refresh int
	var jobConfig jobrender.Config
	flag.StringVar(&namespace, "namespace", os.Getenv("WATCH_NAMESPACE"), "Required namespace to watch (controller must run in it)")
	flag.StringVar(&root, "artifact-root", "/var/lib/arprt", "PVC artifact repository root")
	flag.StringVar(&probes, "health-probe-bind-address", ":8081", "Health and readiness listener")
	flag.StringVar(&listen, "byos-bind-address", ":8080", "BYOS listener")
	flag.StringVar(&publicURL, "public-base-url", os.Getenv("PUBLIC_BASE_URL"), "Required absolute public HTTP(S) URL including external path prefix")
	flag.BoolVar(&enableSetup, "enable-setup", true, "Accept setup and provision missing device credentials (enabled by default)")
	flag.BoolVar(&strips, "proxy-strips-prefix", false, "Proxy strips the configured public URL path prefix")
	flag.IntVar(&refresh, "device-refresh-seconds", 300, "Device polling interval, independent of screen freshness")
	flag.StringVar(&jobConfig.PVC, "job-pvc", os.Getenv("JOB_PVC"), "Shared RWO PVC claim; empty disables Job scheduling")
	flag.StringVar(&jobConfig.ControllerPod, "controller-pod", os.Getenv("POD_NAME"), "Controller Pod for same-node Job affinity validation")
	flag.Int64Var(&jobConfig.DeadlineSeconds, "job-deadline-seconds", 120, "Maximum renderer Job execution duration")
	flag.IntVar(&jobConfig.MaxActive, "job-concurrency", 4, "Maximum active renderer Jobs")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	if namespace == "" || len(validation.IsDNS1123Label(namespace)) > 0 {
		return fmt.Errorf("--namespace must specify a valid namespace")
	}
	if podNamespace := os.Getenv("POD_NAMESPACE"); podNamespace != "" && podNamespace != namespace {
		return fmt.Errorf("controller must run in the watched namespace to share its PVC")
	}
	repo, err := artifact.NewPVC(root)
	if err != nil {
		return fmt.Errorf("initialize artifact repository: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		return err
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		return err
	}
	shutdown := 20 * time.Second
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Cache:                   cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}, ReaderFailOnMissingInformer: true},
		Metrics:                 metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:  probes,
		GracefulShutdownTimeout: &shutdown,
	})
	if err != nil {
		return err
	}
	if jobConfig.PVC != "" {
		jobConfig.Namespace = namespace
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := jobConfig.ValidateTopology(ctx, mgr.GetAPIReader(), root)
		cancel()
		if err != nil {
			return fmt.Errorf("Job storage topology: %w", err)
		}
		store, err := jobrender.NewStore(root)
		if err != nil {
			return err
		}
		jobs := &jobrender.Reconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Repository: repo, Store: store, Config: jobConfig, Recorder: mgr.GetEventRecorder("job-renderer")}
		if err := jobs.SetupWithManager(mgr); err != nil {
			return err
		}
		if err := mgr.Add(jobs); err != nil {
			return err
		}
	}
	if err := (&controller.ScreenReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), JobEnabled: jobConfig.PVC != ""}).SetupWithManager(mgr); err != nil {
		return err
	}
	server, err := byos.NewServer(byos.Config{Namespace: namespace, PublicBaseURL: publicURL, ProxyStripsPrefix: strips, EnableSetup: enableSetup, RefreshSeconds: refresh}, mgr.GetAPIReader(), mgr.GetClient(), repo)
	if err != nil {
		return err
	}
	if err := (&controller.DeviceReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), AuthReader: mgr.GetAPIReader(), Resolver: assignment.Explicit{Reader: mgr.GetAPIReader()}, Identity: server.Identity}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.Add(&byos.Listener{Address: listen, Handler: server}); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("cache", func(_ *http.Request) error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("cache not synchronized")
		}
		return nil
	}); err != nil {
		return err
	}
	ctrl.Log.Info("Starting BYOS controller", "namespace", namespace, "setupEnabled", enableSetup, "jobScheduling", jobConfig.PVC != "")
	return mgr.Start(ctrl.SetupSignalHandler())
}
