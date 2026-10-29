//nolint:goconst // Keep flag inputs and expected values together.
package cmd

import (
	"kubocd/internal/controller"
	"testing"
	"time"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type releaseConstructionManager struct {
	ctrl.Manager
	kubeClient client.Client
	recorder   record.EventRecorder
}

func (m releaseConstructionManager) GetClient() client.Client { return m.kubeClient }
func (m releaseConstructionManager) GetEventRecorderFor(string) record.EventRecorder {
	return m.recorder
}

func TestControllerConditionCELFlagsReachReconciler(t *testing.T) {
	saved := controllerParams
	flags := controllerCmd.PersistentFlags()
	costChanged := flags.Lookup("conditionCelCostLimit").Changed
	timeoutChanged := flags.Lookup("conditionCelTimeout").Changed
	t.Cleanup(func() {
		controllerParams = saved
		flags.Lookup("conditionCelCostLimit").Changed = costChanged
		flags.Lookup("conditionCelTimeout").Changed = timeoutChanged
	})
	manager := releaseConstructionManager{kubeClient: fake.NewClientBuilder().Build(), recorder: record.NewFakeRecorder(1)}
	for _, tc := range []struct {
		name string
		args []string
		want controller.ConditionLimits
	}{
		{name: "defaults", want: controller.ConditionLimits{CostLimit: 1000000, Timeout: time.Second}},
		{name: "custom", args: []string{"--conditionCelCostLimit=19", "--conditionCelTimeout=137ms"},
			want: controller.ConditionLimits{CostLimit: 19, Timeout: 137 * time.Millisecond}},
		{name: "cost disabled", args: []string{"--conditionCelCostLimit=0", "--conditionCelTimeout=29ms"},
			want: controller.ConditionLimits{Timeout: 29 * time.Millisecond}},
		{name: "timeout disabled", args: []string{"--conditionCelCostLimit=83", "--conditionCelTimeout=0"},
			want: controller.ConditionLimits{CostLimit: 83}},
		{name: "both disabled", args: []string{"--conditionCelCostLimit=0", "--conditionCelTimeout=0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controllerParams = saved
			if err := flags.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			reconciler := newReleaseReconciler(manager, nil, t.TempDir(), nil, nil)
			if reconciler.ConditionLimits == nil {
				t.Fatal("controller flags did not reach the ReleaseReconciler: ConditionLimits is nil")
			}
			if *reconciler.ConditionLimits != tc.want {
				t.Fatalf("reconciler condition limits = %+v, want %+v", *reconciler.ConditionLimits, tc.want)
			}
		})
	}
}
