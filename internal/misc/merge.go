package misc

import "fmt"

// MergeMaps merge two maps and return a new one.
// Second parameter map will override the first one
// From https://github.com/helm/helm/blob/v3.14.1/pkg/cli/values/options.go
func MergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if v, ok := v.(map[string]interface{}); ok {
			if bv, ok := out[k]; ok {
				if bv, ok := bv.(map[string]interface{}); ok {
					out[k] = MergeMaps(bv, v)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}

type ClashError interface {
	error
}
type clashError struct {
	prefix string
	path   string
	v1     interface{}
	v2     interface{}
}

func (c *clashError) Error() string {
	return fmt.Sprintf("%sclash error at: %s (%v != %v)", c.prefix, c.path, c.v1, c.v2)
}

var _ ClashError = &clashError{}

// MergeMapsCheck merge two maps and return a new one.
// Second parameter map will override the first one
// This version return a list of clash
func MergeMapsCheck(a, b map[string]interface{}, path string, errorPrefix string, errors []error) (map[string]interface{}, []error) {
	out := make(map[string]interface{}, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if v, ok := v.(map[string]interface{}); ok {
			if bv, ok := out[k]; ok {
				if bv, ok := bv.(map[string]interface{}); ok {
					out[k], errors = MergeMapsCheck(bv, v, path+"/"+k, errorPrefix, errors)
					continue
				}
			}
		}
		if _, exists := out[k]; exists {
			if out[k] != v { // Not a clash if values are equals
				errors = append(errors, &clashError{
					prefix: errorPrefix,
					path:   path + "/" + k,
					v1:     out[k],
					v2:     v,
				})
			}
		}
		out[k] = v
	}
	return out, errors
}
