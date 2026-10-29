package kubopackage

import (
	"errors"
	kv1alpha1 "kubocd/api/v1alpha1"
	"strings"
)

// ErrConditionPolicy identifies a condition outside the allowed kinds or namespace.
var ErrConditionPolicy = errors.New("only core Secret and ConfigMap conditions in the Release target namespace are allowed")

// CoreKind returns the canonical core Secret or ConfigMap kind, or ErrConditionPolicy.
func (c *ConditionRendered) CoreKind() (kv1alpha1.Kind, error) {
	if c == nil || c.Group != "" {
		return "", ErrConditionPolicy
	}
	for _, ch := range c.Kind {
		if ch > 127 {
			return "", ErrConditionPolicy
		}
	}
	switch strings.ToLower(string(c.Kind)) {
	case "secret":
		return "Secret", nil
	case "configmap":
		return "ConfigMap", nil
	default:
		return "", ErrConditionPolicy
	}
}
