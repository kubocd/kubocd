/*
Copyright 2025 Kubotal

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/cache"
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"kubocd/internal/global"
	"kubocd/internal/k8sapi"
	"kubocd/internal/misc"
	"kubocd/internal/rolestore"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

	allowDNSLookups bool

	conditionLimits       controller.ConditionLimits
	packageLimits         controller.PackageLimits
	maxCachedPackages     int
	maxCachedPackagesSize int64

	featureGates []string
	// Resolved from featureGates by PersistentPreRun. Always hold an entry for each known gate.
	gates map[string]bool

	ociRepositoryOptions controller.OCIRepositoryOptions
}

func newReleaseReconciler(mgr ctrl.Manager, packageFetcher *controller.PackageFetcher, serverRoot string,
	theConfigStore configstore.ConfigStore, roleStore rolestore.RoleStore) *controller.ReleaseReconciler {
	return &controller.ReleaseReconciler{
		Client: mgr.GetClient(),
		//nolint:staticcheck // ReleaseReconciler uses record.EventRecorder for core/v1 events.
		EventRecorder:   mgr.GetEventRecorderFor("release"),
		Logger:          controllerRootLog.WithName("ReleaseReconciler"),
		Fetcher:         packageFetcher,
		ServerRoot:      serverRoot,
		HelmRepoAdvAddr: controllerParams.helmRepoAdvAddr,
		PackageCache: cache.NewCache(time.Second*60, controllerParams.maxCachedPackages,
			controllerParams.maxCachedPackagesSize, controllerRootLog.WithName("PackageCache")),
		ConfigStore:          theConfigStore,
		RoleStore:            roleStore,
		AllowDNSLookups:      controllerParams.allowDNSLookups,
		ConditionLimits:      &controllerParams.conditionLimits,
		ConnectionsGate:      controllerParams.gates[featureGateConnections],
		OCIRepositoryOptions: controllerParams.ociRepositoryOptions,
	}
}

// WARNING: All features behind a gate are EXPERIMENTAL. Their resources, fields and behavior may change, or be
// removed, in any future release without prior notice and without a migration path.

const featureGateConnections = "connections"
const featureGateReplications = "replications"

var knownFeatureGates = []string{featureGateConnections, featureGateReplications}

// parseFeatureGates convert the '--featureGates' entries ('<name>=<bool>') in a map, with all known gates defined.
func parseFeatureGates(entries []string) (map[string]bool, error) {
	gates := make(map[string]bool, len(knownFeatureGates))
	for _, name := range knownFeatureGates {
		gates[name] = false
	}
	for _, entry := range entries {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			return nil, fmt.Errorf("invalid feature gate '%s': expected '<name>=true|false'", entry)
		}
		if _, ok := gates[name]; !ok {
			return nil, fmt.Errorf("unknown feature gate '%s'. Valid names are: %s", name, strings.Join(knownFeatureGates, ", "))
		}
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("invalid value '%s' for feature gate '%s': expected true or false", value, name)
		}
		gates[name] = enabled
	}
	return gates, nil
}

func init() {
	registerConditionFlags(controllerCmd, &controllerParams.conditionLimits)
	controllerCmd.PersistentFlags().BoolVar(&controllerParams.ociRepositoryOptions.AllowRedirectVerifyDrop,
		"allowRedirectVerifyDrop", false,
		"Allow a matching packageRedirect without verify to remove Release signature verification.")

	controllerCmd.PersistentFlags().BoolVar(&controllerParams.ociRepositoryOptions.AllowReleaseInsecureSource,
		"allowReleaseInsecureSource", false,
		"Allow Release package sources to use insecure HTTP without a matching packageRedirect.")

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

	controllerCmd.PersistentFlags().BoolVar(&controllerParams.allowDNSLookups, "allowDNSLookups", false,
		"Let the templates of all packages resolve host names with resolveDNS. Release parameters never can.")

	controllerCmd.PersistentFlags().Int64Var(&controllerParams.packageLimits.ArtifactSize, "maxPackageArtifactSize",
		controller.DefaultPackageLimits.ArtifactSize,
		"The most bytes of a package archive, as source-controller serves it. A larger one is refused.")
	controllerCmd.PersistentFlags().Int64Var(&controllerParams.packageLimits.ContentSize, "maxPackageContentSize",
		controller.DefaultPackageLimits.ContentSize,
		"The most bytes that the files of a package archive hold once extracted. More is refused.")
	controllerCmd.PersistentFlags().IntVar(&controllerParams.packageLimits.Entries, "maxPackageEntries",
		controller.DefaultPackageLimits.Entries,
		"The most entries (files, folders, links) of a package archive. More are refused.")
	controllerCmd.PersistentFlags().Int64Var(&controllerParams.packageLimits.ManifestSize, "maxPackageManifestSize",
		controller.DefaultPackageLimits.ManifestSize,
		"The most bytes of the original.yaml of a package, and of its status.yaml. A larger one is refused.")
	controllerCmd.PersistentFlags().IntVar(&controllerParams.maxCachedPackages, "maxCachedPackages", 100,
		"The most packages kept loaded in memory. Beyond, the oldest one is loaded again from its files when needed.")
	controllerCmd.PersistentFlags().Int64Var(&controllerParams.maxCachedPackagesSize, "maxCachedPackagesSize", 4<<20,
		"The most bytes of original.yaml and status.yaml that the packages kept loaded in memory come from.")

	controllerCmd.PersistentFlags().StringSliceVar(&controllerParams.featureGates, "featureGates", nil,
		fmt.Sprintf("Comma separated list of EXPERIMENTAL features to activate, as '<name>=true|false'. Valid names: %s", strings.Join(knownFeatureGates, ", ")))
	controllerCmd.Hidden = true
}

var controllerCmd = &cobra.Command{
	Use:   "controller",
	Short: "Run controller",
	Args:  cobra.NoArgs,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if err := controllerParams.conditionLimits.Validate(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		var err error
		controllerRootLog, err = misc.HandleLog(&controllerParams.logConfig)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Unable to load logging configuration: %v\n", err)
			os.Exit(2)
		}
		controllerParams.gates, err = parseFeatureGates(controllerParams.featureGates)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Unable to parse feature gates: %v\n", err)
			os.Exit(2)
		}
		if err := controllerParams.packageLimits.Validate(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Invalid package limits: %v\n", err)
			os.Exit(2)
		}
		if controllerParams.maxCachedPackages <= 0 || controllerParams.maxCachedPackagesSize <= 0 ||
			controllerParams.maxCachedPackagesSize > math.MaxInt64/2 {
			_, _ = fmt.Fprintln(os.Stderr, "Invalid package cache: both of its limits must be positive, and not too large")
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

		// The Metrics options configure the server. More info:
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
			// can access the metrics endpoint. More info:
			// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
			metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
		}

		// If the certificate is not specified, controller-runtime will automatically
		// generate self-signed certificates for the metrics server. While convenient for development and testing,
		// this setup is not recommended for production.
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
		packageFetcher := controller.NewPackageFetcher(controllerParams.packageLimits, fetchRetry,
			controllerParams.sourceControllerOverride)

		serverRoot := path.Join(controllerParams.rootDataFolder, "server")

		// We need to set up our own kubeClient, as mgr.GetClient() will not be usable until manager is started
		kubeClient, err := k8sapi.GetKubeClientFromConfig(ctrl.GetConfigOrDie(), scheme)
		if err != nil {
			setupLog.Error(err, "unable to create kube client")
			os.Exit(1)
		}

		theConfigStore := configstore.New()
		err = theConfigStore.Init(context.Background(), kubeClient, myPodNamespace)
		if err != nil {
			setupLog.Error(err, "error while loading config resources")
			os.Exit(1)
		}
		roleStore := rolestore.New(theConfigStore, controllerRootLog.WithName("roleStore"))

		// --------------------------------------------------------------- EXPERIMENTAL 'replications' feature gate
		// Nothing outside of the Replication controller uses its index or its map functions, so the whole subsystem
		// is gated. When off, the cluster wide Secret/ConfigMap watches are not set up either.

		if controllerParams.gates[featureGateReplications] {
			controllerRootLog.Info("EXPERIMENTAL feature gate is activated", "gate", featureGateReplications)

			// ----------------------------------------------------------------------------- Replication controller setup

			// Create an index to retrieve a Replication from a Secret/ConfigMap in an efficient way.
			// A Replication is indexed on both its source and its destination, as a modification of any of
			// them must be reverted/propagated.
			err = mgr.GetFieldIndexer().IndexField(context.Background(), &kubocdv1alpha1.Replication{}, controller.ResourceIndexOnReplication, func(rawObj client.Object) []string {
				return controller.ReplicationResourceKeys(rawObj.(*kubocdv1alpha1.Replication))
			})
			if err != nil {
				setupLog.Error(err, "Unable to index Replication by resource")
				os.Exit(1)
			}

			replicationReconciler := &controller.ReplicationReconciler{
				Client:        mgr.GetClient(),
				EventRecorder: mgr.GetEventRecorderFor("replication"),
				Logger:        controllerRootLog.WithName("replicationReconciler"),
			}

			err = ctrl.NewControllerManagedBy(mgr).
				For(&kubocdv1alpha1.Replication{}).
				Named("kubocd-replication-controller").
				// Same as the controller-runtime default, but with the delay capped to our reconcile period,
				// instead of the default 1000s. This way, a Replication waiting for an external condition
				// (typically its destination namespace to be created) is retried at least every period.
				WithOptions(ctrlcontroller.Options{
					RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Millisecond*5, controller.ReplicationReconcilePeriod),
				}).
				Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
					findReplicationFromResource(mgr.GetClient(), controllerRootLog, controller.KindSecret))).
				Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(
					findReplicationFromResource(mgr.GetClient(), controllerRootLog, controller.KindConfigMap))).
				Complete(replicationReconciler)
			if err != nil {
				setupLog.Error(err, "unable to create controller", "controller", "replication")
				os.Exit(1)
			}
		}

		// ------------------------------------------------------------------------------- Connection subsystem indexes
		// These indexes are also used by the Release controller, to lookup its input connections. So, they are
		// registered even when the 'connections' feature gate is off, for the lookup to return an empty list
		// instead of failing.

		// Create an index to retrieve a Connection from a Contract in an efficient way
		// index connection by contract
		err = mgr.GetFieldIndexer().IndexField(context.Background(), &kubocdv1alpha1.Connection{}, controller.ContractIndexOnConnection, func(rawObj client.Object) []string {
			connection := rawObj.(*kubocdv1alpha1.Connection)
			return []string{connection.Spec.Contract}
		})
		if err != nil {
			setupLog.Error(err, "Unable to index Connection by Contract")
			os.Exit(1)
		}

		// Create an index to retrieve a ClusterConnection from a Contract in an efficient way
		// index connection by contract
		err = mgr.GetFieldIndexer().IndexField(context.Background(), &kubocdv1alpha1.ClusterConnection{}, controller.ContractIndexOnClusterConnection, func(rawObj client.Object) []string {
			clusterConnection := rawObj.(*kubocdv1alpha1.ClusterConnection)
			return []string{clusterConnection.Spec.Contract}
		})
		if err != nil {
			setupLog.Error(err, "Unable to index clusterConnection by Contract")
			os.Exit(1)
		}

		// ---------------------------------------------------------------- EXPERIMENTAL 'connections' feature gate
		// The Contract/Connection reconcilers are only started when the gate is on. Without them, no (Cluster)Connection
		// is ever validated, so none reaches the READY state and no Release consumes one.

		if controllerParams.gates[featureGateConnections] {
			controllerRootLog.Info("EXPERIMENTAL feature gate is activated", "gate", featureGateConnections)

			// ------------------------------------------------------------------------------ Contract controller setup

			contractReconciler := &controller.ContractReconciler{
				Client:        mgr.GetClient(),
				EventRecorder: mgr.GetEventRecorderFor("contract"),
				Logger:        controllerRootLog.WithName("contractReconciler"),
			}

			err = ctrl.NewControllerManagedBy(mgr).
				For(&kubocdv1alpha1.Contract{}).
				Named("kubocd-contract-controller").
				Complete(contractReconciler)
			if err != nil {
				setupLog.Error(err, "unable to create controller", "controller", "contract")
				os.Exit(1)
			}

			// ----------------------------------------------------------------------- ClusterContract controller setup

			clusterContractReconciler := &controller.ClusterContractReconciler{
				Client:        mgr.GetClient(),
				EventRecorder: mgr.GetEventRecorderFor("clusterContract"),
				Logger:        controllerRootLog.WithName("clusterContractReconciler"),
			}

			err = ctrl.NewControllerManagedBy(mgr).
				For(&kubocdv1alpha1.ClusterContract{}).
				Named("kubocd-cluster-contract-controller").
				Complete(clusterContractReconciler)
			if err != nil {
				setupLog.Error(err, "unable to create controller", "controller", "clusterContract")
				os.Exit(1)
			}

			// ---------------------------------------------------------------------------- Connection controller setup

			connectionReconciler := &controller.ConnectionReconciler{
				Client:        mgr.GetClient(),
				EventRecorder: mgr.GetEventRecorderFor("connection"),
				Logger:        controllerRootLog.WithName("connectionReconciler"),
			}

			err = ctrl.NewControllerManagedBy(mgr).
				For(&kubocdv1alpha1.Connection{}).
				Named("kubocd-connection-controller").
				Watches(&kubocdv1alpha1.Contract{}, handler.EnqueueRequestsFromMapFunc(
					findConnectionFromContract(mgr.GetClient(), controllerRootLog))).
				Watches(&kubocdv1alpha1.ClusterContract{}, handler.EnqueueRequestsFromMapFunc(
					findConnectionFromContract(mgr.GetClient(), controllerRootLog))).
				Complete(connectionReconciler)
			if err != nil {
				setupLog.Error(err, "unable to create controller", "controller", "connection")
				os.Exit(1)
			}

			// --------------------------------------------------------------------- ClusterConnection controller setup

			clusterConnectionReconciler := &controller.ClusterConnectionReconciler{
				Client:        mgr.GetClient(),
				EventRecorder: mgr.GetEventRecorderFor("clusterConnection"),
				Logger:        controllerRootLog.WithName("clusterConnectionReconciler"),
			}

			err = ctrl.NewControllerManagedBy(mgr).
				For(&kubocdv1alpha1.ClusterConnection{}).
				Named("kubocd-cluster-connection-controller").
				Watches(&kubocdv1alpha1.ClusterContract{}, handler.EnqueueRequestsFromMapFunc(
					findClusterConnectionFromClusterContract(mgr.GetClient(), controllerRootLog))).
				Complete(clusterConnectionReconciler)
			if err != nil {
				setupLog.Error(err, "unable to create controller", "controller", "clusterConnection")
				os.Exit(1)
			}
		}

		// -------------------------------------------------------------------------------------- Config controller setup
		configPublished := make(chan event.GenericEvent, 1)
		configReconciler := &controller.ConfigReconciler{
			Client:         mgr.GetClient(),
			EventRecorder:  mgr.GetEventRecorderFor("config"),
			Logger:         controllerRootLog.WithName("ConfigReconciler"),
			ConfigStore:    theConfigStore,
			MyPodNamespace: myPodNamespace,
			Published:      configPublished,
		}

		err = ctrl.NewControllerManagedBy(mgr).
			For(&kubocdv1alpha1.Config{}).
			Named("kubocd-config").
			Complete(configReconciler)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Config")
			os.Exit(1)
		}

		// ---------------------------------------------------------------------------------------------------- Release controller setup
		if err := indexReleases(mgr.GetFieldIndexer()); err != nil {
			setupLog.Error(err, "Unable to index the Releases")
			os.Exit(1)
		}

		if controllerParams.allowDNSLookups {
			controllerRootLog.Info("Package templates may resolve host names with resolveDNS")
		}
		releaseReconciler := newReleaseReconciler(mgr, packageFetcher, serverRoot, theConfigStore, roleStore)

		err = releaseController(mgr, releaseReconciler, theConfigStore, configPublished, controllerRootLog)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Release")
			os.Exit(1)
		}

		// -------------------------------------------------------------------------------------- Context controller setup

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
			setupLog.Error(err, "Unable to index Context by parent Context")
			os.Exit(1)
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
			Watches(&kubocdv1alpha1.Context{}, handler.EnqueueRequestsFromMapFunc(
				findChildFromParent(mgr.GetClient(), controllerRootLog))).
			Complete(contextReconciler)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Context")
			os.Exit(1)
		}

		// ---------------------------------------------------------------------------- Misc index creation
		// Create an index to retrieve Connections from a release in an efficient way
		// index connection by owner release (output connection)
		//
		// Used by func (r *ReleaseReconciler) FindOutputConnectionFromRelease(ctx context.Context, release client.Object, logger logr.Logger) []string
		//
		err = mgr.GetFieldIndexer().IndexField(context.Background(), &kubocdv1alpha1.Connection{}, controller.ReleaseIndexOnOutputConnection, func(rawObj client.Object) []string {
			return controller.OutputConnectionReleaseKeys(rawObj.(*kubocdv1alpha1.Connection))
		})
		if err != nil {
			setupLog.Error(err, "Unable to index Connection by Release")
			os.Exit(1)
		}

		// ---------------------------------------------------------------------------- Misc index creation
		// Create an index to retrieve ClusterConnections from a release in an efficient way
		// index clusterConnection by owner release (output connection)
		//
		// Used by func (r *ReleaseReconciler) FindOutputClusterConnectionFromRelease(ctx context.Context, release client.Object) []string
		//
		err = mgr.GetFieldIndexer().IndexField(context.Background(), &kubocdv1alpha1.ClusterConnection{}, controller.ReleaseIndexOnOutputClusterConnection, func(rawObj client.Object) []string {
			return controller.OutputClusterConnectionReleaseKeys(rawObj.(*kubocdv1alpha1.ClusterConnection))
		})
		if err != nil {
			setupLog.Error(err, "Unable to index ClusterConnection by Release")
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
	err := http.ListenAndServe(address, helmRepositoryHandler(path, logger))
	if err != nil {
		logger.Error(err, "file server error")
	}
}

// helmRepositoryHandler serves the files under root, and answers 404 for a
// folder, as for a missing file.
func helmRepositoryHandler(root string, logger logr.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", RequestDumpMiddleware(http.FileServer(filesOnly{http.Dir(root)}), logger.WithName("repoServer")))
	return mux
}

// filesOnly opens the files of a file system, and none of its folders.
type filesOnly struct {
	fileSystem http.FileSystem
}

func (f filesOnly) Open(name string) (http.File, error) {
	file, err := f.fileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.IsDir() {
		_ = file.Close()
		return nil, fs.ErrNotExist
	}
	return file, nil
}

// RequestDumpMiddleware logs the incoming request
func RequestDumpMiddleware(next http.Handler, logger logr.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.V(1).Info("HTTP Request", "path", r.URL.Path, "method", r.Method)
		// Pass to next handler
		next.ServeHTTP(w, r)
	})
}
