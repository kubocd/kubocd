package cmd

import (
	"kubocd/internal/controller"

	"github.com/spf13/cobra"
)

func registerConditionFlags(cmd *cobra.Command, limits *controller.ConditionLimits) {
	cmd.PersistentFlags().Int64Var(&limits.CostLimit, "conditionCelCostLimit", controller.DefaultConditionLimits.CostLimit,
		"Maximum CEL cost per condition evaluation; 0 disables the cost limit, negative values are refused.")
	cmd.PersistentFlags().DurationVar(&limits.Timeout, "conditionCelTimeout", controller.DefaultConditionLimits.Timeout,
		"Timeout per condition CEL evaluation; 0 disables this timeout (caller cancellation/deadlines still apply); "+
			"negative values are refused.")
}
