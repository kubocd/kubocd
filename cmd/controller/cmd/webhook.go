package cmd

import (
	"crypto/tls"
	"github.com/spf13/cobra"
	"kubocd/internal/global"
	webhookkubocdv1alpha1 "kubocd/internal/webhook/v1alpha1"
	"os"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var webhookParams struct {
	probeAddr   string
	enableHTTP2 bool

	metricsAddr     string
	secureMetrics   bool
	metricsCertPath string
	metricsCertName string
	metricsCertKey  string

	webhookCertPath string
	webhookCertName string
	webhookCertKey  string
}

func init() {
	webhookCmd.PersistentFlags().StringVar(&webhookParams.probeAddr, "healthProbeBindAddress", ":8081", "The address the probe endpoint binds to.")
	webhookCmd.PersistentFlags().BoolVar(&webhookParams.enableHTTP2, "enableHttp2", false, "If set, HTTP/2 will be enabled for the metrics and webhook servers")

	webhookCmd.PersistentFlags().StringVar(&webhookParams.metricsAddr, "metricsBindAddress", "0", "The address the metrics endpoint binds to. Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	webhookCmd.PersistentFlags().BoolVar(&webhookParams.secureMetrics, "metricsSecure", true, "If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	webhookCmd.PersistentFlags().StringVar(&webhookParams.metricsCertPath, "metricsCertPath", "", "The directory that contains the metrics server certificate.")
	webhookCmd.PersistentFlags().StringVar(&webhookParams.metricsCertName, "metricsCertName", "tls.crt", "The name of the metrics server certificate file.")
	webhookCmd.PersistentFlags().StringVar(&webhookParams.metricsCertKey, "metricsCertKey", "tls.key", "The name of the metrics server key file.")

	webhookCmd.PersistentFlags().StringVar(&webhookParams.webhookCertPath, "webhookCertPath", "", "The directory that contains the webhook certificate.")
	webhookCmd.PersistentFlags().StringVar(&webhookParams.webhookCertName, "webhookCertName", "tls.crt", "The name of the webhook certificate file.")
	webhookCmd.PersistentFlags().StringVar(&webhookParams.webhookCertKey, "webhookCertKey", "tls.key", "The name of the webhook key file.")
}

var webhookCmd = &cobra.Command{
	Use:   "webhook",
	Short: "Run webhook server",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		var tlsOpts []func(*tls.Config)

		ctrl.SetLogger(rootLog)
		setupLog := ctrl.Log.WithName("setup")

		rootLog.Info("kubocd webhook server start", "version", global.Version, "build", global.BuildTs, "logLevel", rootParams.logConfig.Level)

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

		if !webhookParams.enableHTTP2 {
			tlsOpts = append(tlsOpts, disableHTTP2)
		}

		// Create watchers for webhooks certificate
		var webhookCertWatcher *certwatcher.CertWatcher

		// Initial webhook TLS options
		webhookTLSOpts := tlsOpts

		if len(webhookParams.webhookCertPath) > 0 {
			setupLog.Info("Initializing webhook certificate watcher using provided certificates",
				"webhook-cert-path", webhookParams.webhookCertPath, "webhook-cert-name", webhookParams.webhookCertName, "webhook-cert-key", webhookParams.webhookCertKey)

			var err error
			webhookCertWatcher, err = certwatcher.New(
				filepath.Join(webhookParams.webhookCertPath, webhookParams.webhookCertName),
				filepath.Join(webhookParams.webhookCertPath, webhookParams.webhookCertKey),
			)
			if err != nil {
				setupLog.Error(err, "Failed to initialize webhook certificate watcher")
				os.Exit(1)
			}

			webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
				config.GetCertificate = webhookCertWatcher.GetCertificate
			})
		}

		webhookServer := webhook.NewServer(webhook.Options{
			TLSOpts: webhookTLSOpts,
		})

		// Create watchers for metrics certificates
		var metricsCertWatcher *certwatcher.CertWatcher

		// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
		// More info:
		// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/metrics/server
		// - https://book.kubebuilder.io/reference/metrics.html
		metricsServerOptions := metricsserver.Options{
			BindAddress:   webhookParams.metricsAddr,
			SecureServing: webhookParams.secureMetrics,
			TLSOpts:       tlsOpts,
		}

		if webhookParams.secureMetrics {
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
		if len(webhookParams.metricsCertPath) > 0 {
			setupLog.Info("Initializing metrics certificate watcher using provided certificates",
				"metricsCertPath", webhookParams.metricsCertPath, "metricsCertName", webhookParams.metricsCertName, "metricsCertKey", webhookParams.metricsCertKey)

			var err error
			metricsCertWatcher, err = certwatcher.New(
				filepath.Join(webhookParams.metricsCertPath, webhookParams.metricsCertName),
				filepath.Join(webhookParams.metricsCertPath, webhookParams.metricsCertKey),
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
			WebhookServer:          webhookServer,
			HealthProbeBindAddress: webhookParams.probeAddr,
			LeaderElection:         false,
		})
		if err != nil {
			setupLog.Error(err, "unable to start manager")
			os.Exit(1)
		}
		if err = webhookkubocdv1alpha1.SetupReleaseWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Release")
			os.Exit(1)
		}
		// +kubebuilder:scaffold:builder

		if metricsCertWatcher != nil {
			setupLog.Info("Adding metrics certificate watcher to manager")
			if err := mgr.Add(metricsCertWatcher); err != nil {
				setupLog.Error(err, "unable to add metrics certificate watcher to manager")
				os.Exit(1)
			}
		}

		if webhookCertWatcher != nil {
			setupLog.Info("Adding webhook certificate watcher to manager")
			if err := mgr.Add(webhookCertWatcher); err != nil {
				setupLog.Error(err, "unable to add webhook certificate watcher to manager")
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

		setupLog.Info("starting manager")
		if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
			setupLog.Error(err, "problem running manager")
			os.Exit(1)
		}

	},
}
