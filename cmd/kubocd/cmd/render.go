package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	kapi "kubocd/api/v1alpha1"
	"kubocd/internal/k8sapi"
	"kubocd/internal/misc"
	"os"
)

var renderParams struct {
	output string
}

func init() {
	renderCmd.Flags().StringVarP(&renderParams.output, "output", "o", "./rendered", "Output directory")
}

var renderCmd = &cobra.Command{
	Use:   "render <Release manifest> [<Application manifest>]",
	Short: "Render a KuboCD release",
	Args:  cobra.RangeArgs(1, 2),

	Run: func(cmd *cobra.Command, args []string) {
		err := func() error {
			output := renderParams.output

			err := misc.SafeEnsureEmpty(output)
			if err != nil {
				return err
			}

			release := &kapi.Release{}
			err = misc.LoadYaml(args[0], release)
			if err != nil {
				return err
			}
			err = groomRelease(release)
			if err != nil {
				return err
			}

			misc.DumpYaml(output, "release.yaml", release)

			//fmt.Println(app.Name)
			_, err = k8sapi.GetKubeClient(scheme)
			if err != nil {
				return err
			}

			//_, err = computeSetting(context.Background(), k8sClient, release.Spec.Settings, release.Namespace)
			//if err != nil {
			//	return err
			//}

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

//func appendSettings(ctx context.Context, client client.Client, newSettings []kapi.NamespacedName, defaultNamespace string, collector []*kapi.Setting) ([]*kapi.Setting, error) {
//	for _, settingNs := range newSettings {
//		if settingNs.Namespace == "" {
//			settingNs.Namespace = defaultNamespace
//		}
//		setting := &kapi.Setting{}
//		err := client.Get(ctx, settingNs.ToObjectKey(), setting)
//		if err != nil {
//			return nil, err
//		}
//		if len(setting.Spec.Parents) > 0 {
//			collector, err = appendSettings(ctx, client, setting.Spec.Parents, setting.Namespace, collector)
//			if err != nil {
//				return nil, err
//			}
//		}
//		collector = append(collector, setting)
//	}
//	return collector, nil
//}
//
//func computeSetting(ctx context.Context, client client.Client, settings []kapi.NamespacedName, defaultNamespace string) (*kapi.Setting, error) {
//	collector := make([]*kapi.Setting, 0)
//	var err error
//	collector, err = appendSettings(ctx, client, settings, defaultNamespace, collector)
//	if err != nil {
//		return nil, err
//	}
//	for _, setting := range collector {
//		fmt.Printf("Setting: %s:%s\n", setting.Name, setting.Namespace)
//	}
//	return nil, nil
//}
