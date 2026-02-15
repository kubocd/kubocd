package misc

// Prune recursively remove all null item of the given map
func Prune(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		if v == nil {
			continue
		}
		if nested, ok := v.(map[string]interface{}); ok {
			pruned := Prune(nested)
			if len(pruned) > 0 {
				result[k] = pruned
			}
			continue
		}
		if slice, ok := v.([]interface{}); ok {
			result[k] = pruneSlice(slice)
			continue
		}
		result[k] = v
	}
	return result
}

func pruneSlice(s []interface{}) []interface{} {
	result := make([]interface{}, 0, len(s))
	for _, v := range s {
		if v == nil {
			continue
		}
		if nested, ok := v.(map[string]interface{}); ok {
			pruned := Prune(nested)
			if len(pruned) > 0 {
				result = append(result, pruned)
			}
			continue
		}
		if slice, ok := v.([]interface{}); ok {
			result = append(result, pruneSlice(slice))
			continue
		}
		result = append(result, v)
	}
	return result
}
