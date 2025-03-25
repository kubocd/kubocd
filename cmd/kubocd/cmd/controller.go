package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/http/fetch"
	"github.com/fluxcd/pkg/tar"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/cache"
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"kubocd/internal/rolestore"
	"net/http"
	"os"
	"path"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"
)

var controllerRootLog logr.Logger

var controllerParams struct {
	logConfig misc.LogConfig

	probeAddr            string
	enableLeaderElection bool
	enableHTTP2          bool

	metricsAddr     string
	secureMetrics   bool
	metricsCertPath string
	metricsCertName string
	metricsCertKey  string

	rootDataFolder           string
	sourceControllerOverride string
	helmRepoAdvAddr          string
	helmRepoBindAddr         string
}

func init() {
	controllerCmd.PersistentFlags().StringVar(&controllerParams.logConfig.Level, "logLevel", "INFO", "Log level")
	controllerCmd.PersistentFlags().StringVar(&controllerParams.logConfig.Mode, "logMode", "dev", "Log mode: 'dev' or 'json'")

	controllerCmd.PersistentFlags().StringVar(&controllerParams.probeAddr, "healthProbeBindAddress", ":8081", "The address the probe endpoint binds to.")
	controllerCmd.PersistentFlags().BoolVar(&controllerParams.enableLeaderElection, "leaderElect", false, "Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	controllerCmd.PersistentFlags().BoolVar(&controllerParams.enableHTTP2, "enableHttp2", false, "If set, HTTP/2 will be enabled for the metrics and webhook servers")

	controllerCmd.PersistentFlags().StringVar(&controllerParams.metricsAddr, "metricsBindAddress", "0", "The address the metrics endpoint binds to. Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	controllerCmd.PersistentFlags().BoolVar(&controllerParams.secureMetrics, "metricsSecure", true, "If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	controllerCmd.PersistentFlags().StringVar(&controllerParams.metricsCertPath, "metricsCertPath", "", "The directory that contains the metrics server certificate.")
	controllerCmd.PersistentFlags().StringVar(&controllerParams.metricsCertName, "metricsCertName", "tls.crt", "The name of the metrics server certificate file.")
	controllerCmd.PersistentFlags().StringVar(&controllerParams.metricsCertKey, "metricsCertKey", "tls.key", "The name of the metrics server key file.")

	controllerCmd.PersistentFlags().StringVar(&controllerParams.rootDataFolder, "rootDataFolder", "/works", "Root data folder")
	controllerCmd.PersistentFlags().StringVar(&controllerParams.sourceControllerOverride, "sourceControllerOverride", "", "Override source controller fetch entry point. In the form <X.X.X.X:PORT")
	controllerCmd.PersistentFlags().StringVar(&controllerParams.helmRepoAdvAddr, "helmRepoAdvAddr", "", "The advertised network address of our helm repository file server.")
	controllerCmd.PersistentFlags().StringVar(&controllerParams.helmRepoBindAddr, "helmRepoBindAddr", ":9090", "The address the static helm repository server binds to.")

}

var controllerCmd = &cobra.Command{
	Use:   "controller",
	Short: "Run controller",
	Args:  cobra.NoArgs,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		var err error
		controllerRootLog, err = misc.HandleLog(&controllerParams.logConfig)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Unable to load logging configuration: %v\n", err)
			os.Exit(2)
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		var tlsOpts []func(*tls.Config)

		ctrl.SetLogger(controllerRootLog)
		setupLog := ctrl.Log.WithName("setup")

		controllerRootLog.Info("kubocd controller start", "version", global.Version, "build", global.BuildTs, "logLevel", controllerParams.logConfig.Level)

		if controllerParams.helmRepoAdvAddr == "" {
			setupLog.Error(nil, "'helmRepoAdvAddr' is required")
			os.Exit(2)
		}

		myPodNamespace := os.Getenv("MY_POD_NAMESPACE")
		if myPodNamespace == "" {
			setupLog.Error(nil, "'MY_POD_NAMESPACE' environment variable must be set")
			os.Exit(2)
		}

		// if the enable-http2 flag is false (the default), http/2 should be disabled
		// due to its vulnerabilities. More specifically, disabling http/2 will
		// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
		// Rapid Reset CVEs. For more information see:
		// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
		// - https://github.com/advisories/GHSA-4374-p667-p6c8
		disableHTTP2 := func(c *tls.Config) {
			setupLog.Info("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		}

		if !controllerParams.enableHTTP2 {
			tlsOpts = append(tlsOpts, disableHTTP2)
		}
		// Create watchers for metrics certificates
		var metricsCertWatcher *certwatcher.CertWatcher

		// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
		// More info:
		// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/metrics/server
		// - https://book.kubebuilder.io/reference/metrics.html
		metricsServerOptions := metricsserver.Options{
			BindAddress:   controllerParams.metricsAddr,
			SecureServing: controllerParams.secureMetrics,
			TLSOpts:       tlsOpts,
		}

		if controllerParams.secureMetrics {
			// FilterProvider is used to protect the metrics endpoint with authn/authz.
			// These configurations ensure that only authorized users and service accounts
			// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
			// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
			metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
		}

		// If the certificate is not specified, controller-runtime will automatically
		// generate self-signed certificates for the metrics server. While convenient for development and testing,
		// this setup is not recommended for production.
		//
		// TODO(user): If you enable certManager, uncomment the following lines:
		// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
		// managed by cert-manager for the metrics server.
		// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
		if len(controllerParams.metricsCertPath) > 0 {
			setupLog.Info("Initializing metrics certificate watcher using provided certificates",
				"metricsCertPath", controllerParams.metricsCertPath, "metricsCertName", controllerParams.metricsCertName, "metricsCertKey", controllerParams.metricsCertKey)

			var err error
			metricsCertWatcher, err = certwatcher.New(
				filepath.Join(controllerParams.metricsCertPath, controllerParams.metricsCertName),
				filepath.Join(controllerParams.metricsCertPath, controllerParams.metricsCertKey),
			)
			if err != nil {
				setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
				os.Exit(1)
			}

			metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
				config.GetCertificate = metricsCertWatcher.GetCertificate
			})
		}

		mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
			Scheme:                 scheme,
			Metrics:                metricsServerOptions,
			HealthProbeBindAddress: controllerParams.probeAddr,
			LeaderElection:         controllerParams.enableLeaderElection,
			LeaderElectionID:       "26cd80d1.kubocd.kubotal.io",
			// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
			// when the Manager ends. This requires the binary to immediately end when the
			// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
			// speeds up voluntary leader transitions as the new leader don't have to wait
			// LeaseDuration time first.
			//
			// In the default scaffold provided, the program ends immediately after
			// the manager stops, so would be fine to enable this option. However,
			// if you are doing or is intended to do any operation such as perform cleanups
			// after the manager stops then its usage might be unsafe.
			// LeaderElectionReleaseOnCancel: true,
		})
		if err != nil {
			setupLog.Error(err, "unable to start manager")
			os.Exit(1)
		}

		fetchRetry := 9
		archiveFetcher := fetch.New(fetch.WithRetries(fetchRetry), fetch.WithMaxDownloadSize(tar.UnlimitedUntarSize),
			fetch.WithUntar(tar.WithMaxUntarSize(tar.UnlimitedUntarSize)), fetch.WithHostnameOverwrite(controllerParams.sourceControllerOverride))

		serverRoot := path.Join(controllerParams.rootDataFolder, "server")

		configStore := configstore.New()
		roleStore := rolestore.New(configStore, controllerRootLog.WithName("roleStore"))

		// ---------------------------------------------------------------------------------------------------- Release controller setup
		// Create an index to retrieve a Release from a context in an efficient way
		// index release by contexts
		const contextIndexOnRelease = "contextIndexOnRelease"
		err = mgr.GetFieldIndexer().IndexField(context.Background(), &kubocdv1alpha1.Release{}, contextIndexOnRelease, func(rawObj client.Object) []string {
			release := rawObj.(*kubocdv1alpha1.Release)
			contexts := make([]string, len(release.Spec.Contexts))
			for i, context := range release.Spec.Contexts {
				ns := context.Namespace
				if ns == "" {
					ns = release.Namespace
				}
				contexts[i] = fmt.Sprintf("%s:%s", ns, context.Name)
			}
			return contexts
		})
		if err != nil {
			setupLog.Error(err, "Unable to index Release by Context")
			os.Exit(1)
		}

		findReleaseFromContext := func(ctx context.Context, kcontext client.Object) []reconcile.Request {
			releases := kubocdv1alpha1.ReleaseList{}
			listOps := &client.ListOptions{
				FieldSelector: fields.OneTermEqualSelector(contextIndexOnRelease, fmt.Sprintf("%s:%s", kcontext.GetNamespace(), kcontext.GetName())),
			}
			err := mgr.GetClient().List(context.Background(), &releases, listOps)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					controllerRootLog.Error(err, "findReleaseFromContext(): Unable to find Context bindings")
				}
				return []reconcile.Request{}
			}
			requests := make([]reconcile.Request, 0, 10)
			for _, item := range releases.Items {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      item.GetName(),
						Namespace: item.GetNamespace(),
					},
				})
			}
			return requests
		}

		releaseReconciler := &controller.ReleaseReconciler{
			Client:           mgr.GetClient(),
			EventRecorder:    mgr.GetEventRecorderFor("release"),
			Logger:           controllerRootLog.WithName("ReleaseReconciler"),
			Fetcher:          archiveFetcher,
			ServerRoot:       serverRoot,
			HelmRepoAdvAddr:  controllerParams.helmRepoAdvAddr,
			ApplicationCache: cache.NewCache(time.Second*60, controllerRootLog.WithName("ApplicationCache")),
			ConfigStore:      configStore,
			RoleStore:        roleStore,
		}

		err = ctrl.NewControllerManagedBy(mgr).
			For(&kubocdv1alpha1.Release{}).
			Named("kubocd-release").
			Owns(&sourcev1b2.OCIRepository{}).
			Owns(&sourcev1.HelmRepository{}).
			Owns(&fluxv2.HelmRelease{}).
			Watches(&kubocdv1alpha1.Context{}, handler.EnqueueRequestsFromMapFunc(findReleaseFromContext)).
			Complete(releaseReconciler)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Release")
			os.Exit(1)
		}
		// -------------------------------------------------------------------------------------- Context controller setup

		const parentIndexOnChild = "parentIndexOnChild"
		err = mgr.GetFieldIndexer().IndexField(context.Background(), &kubocdv1alpha1.Context{}, parentIndexOnChild, func(rawObj client.Object) []string {
			child := rawObj.(*kubocdv1alpha1.Context)
			parents := make([]string, len(child.Spec.Parents))
			for i, parent := range child.Spec.Parents {
				ns := parent.Namespace
				if ns == "" {
					ns = child.Namespace
				}
				parents[i] = fmt.Sprintf("%s:%s", ns, parent.Name)
			}
			return parents
		})
		if err != nil {
			setupLog.Error(err, "Unable to index Release by Context")
			os.Exit(1)
		}

		findChildFromParent := func(ctx context.Context, parentContext client.Object) []reconcile.Request {
			children := kubocdv1alpha1.ContextList{}
			listOps := &client.ListOptions{
				FieldSelector: fields.OneTermEqualSelector(parentIndexOnChild, fmt.Sprintf("%s:%s", parentContext.GetNamespace(), parentContext.GetName())),
			}
			err := mgr.GetClient().List(context.Background(), &children, listOps)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					controllerRootLog.Error(err, "findChildFromParent(): Unable to find Context bindings")
				}
				return []reconcile.Request{}
			}
			requests := make([]reconcile.Request, 0, 10)
			for _, item := range children.Items {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      item.GetName(),
						Namespace: item.GetNamespace(),
					},
				})
			}
			return requests
		}

		contextReconciler := &controller.ContextReconciler{
			Client: mgr.GetClient(),
			//Scheme:        mgr.GetScheme(),
			EventRecorder: mgr.GetEventRecorderFor("context"),
			Logger:        controllerRootLog.WithName("ContextReconciler"),
		}

		err = ctrl.NewControllerManagedBy(mgr).
			For(&kubocdv1alpha1.Context{}).
			Named("kubocd-context").
			Watches(&kubocdv1alpha1.Context{}, handler.EnqueueRequestsFromMapFunc(findChildFromParent)).
			Complete(contextReconciler)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Context")
			os.Exit(1)
		}

		// -------------------------------------------------------------------------------------- Config controller setup
		configReconciler := &controller.ConfigReconciler{
			Client:         mgr.GetClient(),
			EventRecorder:  mgr.GetEventRecorderFor("config"),
			Logger:         controllerRootLog.WithName("ConfigReconciler"),
			ConfigStore:    configStore,
			MyPodNamespace: myPodNamespace,
		}

		err = ctrl.NewControllerManagedBy(mgr).
			For(&kubocdv1alpha1.Config{}).
			Named("kubocd-config").
			Complete(configReconciler)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Config")
			os.Exit(1)
		}

		// ----------------------------------------------------------------------------------------------------
		if metricsCertWatcher != nil {
			setupLog.Info("Adding metrics certificate watcher to manager")
			if err := mgr.Add(metricsCertWatcher); err != nil {
				setupLog.Error(err, "unable to add metrics certificate watcher to manager")
				os.Exit(1)
			}
		}

		if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
			setupLog.Error(err, "unable to set up health check")
			os.Exit(1)
		}
		if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
			setupLog.Error(err, "unable to set up ready check")
			os.Exit(1)
		}

		go func() {
			// Block until our controller manager is elected leader. We presume our
			// entire process will terminate if we lose leadership, so we don't need
			// to handle that.
			<-mgr.Elected()

			startFileServer(serverRoot, controllerParams.helmRepoBindAddr, controllerRootLog.WithName("helmRepositoryServer"))
		}()

		setupLog.Info("starting manager")
		if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
			setupLog.Error(err, "problem running manager")
			os.Exit(1)
		}

	},
}

func startFileServer(path string, address string, logger logr.Logger) {
	logger.Info("starting helm repository file server", "bindAddress", address, "path", path)
	fs := http.FileServer(http.Dir(path))
	mux := http.NewServeMux()
	//mux.Handle("/", fs)
	mux.Handle("/", RequestDumpMiddleware(fs, logger.WithName("repoServer")))
	err := http.ListenAndServe(address, mux)
	if err != nil {
		logger.Error(err, "file server error")
	}
}

// RequestDumpMiddleware logs the incoming request
func RequestDumpMiddleware(next http.Handler, logger logr.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.V(1).Info("HTTP Request", "path", r.URL.Path, "method", r.Method)
		// Pass to next handler
		next.ServeHTTP(w, r)
	})
}
