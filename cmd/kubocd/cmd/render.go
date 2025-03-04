package cmd

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	kapi "kubocd/api/v1alpha1"
	"kubocd/internal/k8sapi"
	"kubocd/internal/misc"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var renderParams struct {
	all      bool
	settings bool
}

func init() {
	renderCmd.PersistentFlags().BoolVar(&renderParams.all, "all", false, "Display all resources")
	renderCmd.PersistentFlags().BoolVar(&renderParams.settings, "renderParams", false, "Display settings")
}

var renderCmd = &cobra.Command{
	Use:   "render <Release manifest>",
	Short: "Render a KuboCD release",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		err := func() error {
			if misc.CountNonZero(renderParams.all, renderParams.settings) == 0 {
				renderParams.all = true
			}
			release := &kapi.Release{}
			err := misc.LoadYaml(args[0], release)
			if err != nil {
				return err
			}
			err = groomRelease(release)
			if err != nil {
				return err
			}
			//fmt.Println(app.Name)
			k8sClient, err := k8sapi.GetKubeClient(scheme)
			if err != nil {
				return err
			}
			_, err = computeSetting(context.Background(), k8sClient, release.Spec.Settings, release.Namespace)
			if err != nil {
				return err
			}

			return nil
		}()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
	},
}

func groomRelease(r *kapi.Release) error {
	if r.Namespace == "" {
		r.Namespace = "default"
	}
	return nil
}

func appendSettings(ctx context.Context, client client.Client, newSettings []kapi.NamespacedName, defaultNamespace string, collector []*kapi.Setting) ([]*kapi.Setting, error) {
	for _, settingNs := range newSettings {
		if settingNs.Namespace == "" {
			settingNs.Namespace = defaultNamespace
		}
		setting := &kapi.Setting{}
		err := client.Get(ctx, settingNs.ToObjectKey(), setting)
		if err != nil {
			return nil, err
		}
		if len(setting.Spec.Parents) > 0 {
			collector, err = appendSettings(ctx, client, setting.Spec.Parents, setting.Namespace, collector)
			if err != nil {
				return nil, err
			}
		}
		collector = append(collector, setting)
	}
	return collector, nil
}

func computeSetting(ctx context.Context, client client.Client, settings []kapi.NamespacedName, defaultNamespace string) (*kapi.Setting, error) {
	collector := make([]*kapi.Setting, 0)
	var err error
	collector, err = appendSettings(ctx, client, settings, defaultNamespace, collector)
	if err != nil {
		return nil, err
	}
	for _, setting := range collector {
		fmt.Printf("Setting: %s:%s\n", setting.Name, setting.Namespace)
	}
	return nil, nil
}
