package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	clustersecretv1 "github.com/satoukick/clustersecret-go/api/v1"
	"github.com/satoukick/clustersecret-go/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrllog.Log.WithName("setup")
	// version is injected at build time via -ldflags "-X ...cmd.version=...".
	// Defaults to "dev" for local `go run` / `go build`.
	version = "dev"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clustersecretv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          = flag.String("metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
		probeAddr            = flag.String("health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
		enableLeaderElection = flag.Bool("leader-elect", false,
			"Enable leader election for controller manager. "+
				"Enabling this will ensure there is only one active controller manager.")
	)
	flag.Parse()

	ctrllog.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))

	mgr, err := manager.New(config.GetConfigOrDie(), manager.Options{
		Scheme:                 scheme,
		Metrics:                server.Options{BindAddress: *metricsAddr},
		HealthProbeBindAddress: *probeAddr,
		LeaderElection:         *enableLeaderElection,
		LeaderElectionID:       "clustersecret.io",
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{},
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.ClusterSecretReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrllog.Log.WithName("controllers").WithName("ClusterSecret"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ClusterSecret")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager", "version", version)
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
