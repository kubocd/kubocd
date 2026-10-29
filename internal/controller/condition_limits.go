package controller

import (
	"errors"
	"time"
)

// ConditionLimits bounds the cost and duration of a condition's CEL evaluation.
type ConditionLimits struct {
	CostLimit int64
	Timeout   time.Duration
}

// DefaultConditionLimits provides the default cost and timeout for each CEL evaluation.
var DefaultConditionLimits = ConditionLimits{CostLimit: 1000000, Timeout: time.Second}

// Validate rejects negative condition limits.
func (l ConditionLimits) Validate() error {
	if l.CostLimit < 0 {
		return errors.New("conditionCelCostLimit must not be negative")
	}
	if l.Timeout < 0 {
		return errors.New("conditionCelTimeout must not be negative")
	}
	return nil
}
