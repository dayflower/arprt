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
	"github.com/dayflower/arprt/internal/controller"
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
	var namespace, root, probes string
	flag.StringVar(&namespace, "namespace", os.Getenv("WATCH_NAMESPACE"), "Required namespace to watch (controller must run in it)")
	flag.StringVar(&root, "artifact-root", "/var/lib/arprt", "PVC artifact repository root")
	flag.StringVar(&probes, "health-probe-bind-address", ":8081", "Health and readiness listener")
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
	if _, err := artifact.NewPVC(root); err != nil {
		return fmt.Errorf("initialize artifact repository: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
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
	if err := (&controller.ScreenReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.DeviceReconciler{Client: mgr.GetClient(), Resolver: assignment.Explicit{Reader: mgr.GetClient()}}).SetupWithManager(mgr); err != nil {
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
	ctrl.Log.Info("Starting Phase 1 controller", "namespace", namespace, "deliveryAvailable", false)
	return mgr.Start(ctrl.SetupSignalHandler())
}
