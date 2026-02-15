package misc

import (
	"reflect"
	"testing"
)

func TestPrune(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name:     "empty map",
			input:    map[string]interface{}{},
			expected: map[string]interface{}{},
		},
		{
			name: "no null values",
			input: map[string]interface{}{
				"name": "hello",
				"port": 8080,
			},
			expected: map[string]interface{}{
				"name": "hello",
				"port": 8080,
			},
		},
		{
			name: "top-level null removed",
			input: map[string]interface{}{
				"name":    "hello",
				"license": nil,
			},
			expected: map[string]interface{}{
				"name": "hello",
			},
		},
		{
			name: "all null values results in empty map",
			input: map[string]interface{}{
				"a": nil,
				"b": nil,
			},
			expected: map[string]interface{}{},
		},
		{
			name: "nested null removed",
			input: map[string]interface{}{
				"global": map[string]interface{}{
					"operator": map[string]interface{}{
						"name": nil,
						"size": 3,
					},
				},
			},
			expected: map[string]interface{}{
				"global": map[string]interface{}{
					"operator": map[string]interface{}{
						"size": 3,
					},
				},
			},
		},
		{
			name: "nested map becomes empty after prune is removed",
			input: map[string]interface{}{
				"global": map[string]interface{}{
					"operator": map[string]interface{}{
						"name": nil,
					},
				},
			},
			expected: map[string]interface{}{},
		},
		{
			name: "deeply nested null",
			input: map[string]interface{}{
				"a": map[string]interface{}{
					"b": map[string]interface{}{
						"c": map[string]interface{}{
							"d": nil,
						},
					},
				},
			},
			expected: map[string]interface{}{},
		},
		{
			name: "mixed null and non-null at various levels",
			input: map[string]interface{}{
				"keep": "value",
				"drop": nil,
				"nested": map[string]interface{}{
					"keep2": 42,
					"drop2": nil,
					"deep": map[string]interface{}{
						"drop3": nil,
					},
				},
			},
			expected: map[string]interface{}{
				"keep": "value",
				"nested": map[string]interface{}{
					"keep2": 42,
				},
			},
		},
		{
			name: "slice values are preserved",
			input: map[string]interface{}{
				"items": []interface{}{"a", "b", "c"},
			},
			expected: map[string]interface{}{
				"items": []interface{}{"a", "b", "c"},
			},
		},
		{
			name: "null elements in slice are removed",
			input: map[string]interface{}{
				"items": []interface{}{"a", nil, "c"},
			},
			expected: map[string]interface{}{
				"items": []interface{}{"a", "c"},
			},
		},
		{
			name: "slice with nested maps containing nulls",
			input: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"name": "first", "val": nil},
					map[string]interface{}{"name": nil},
				},
			},
			expected: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"name": "first"},
				},
			},
		},
		{
			name: "empty string and zero values are kept",
			input: map[string]interface{}{
				"empty":  "",
				"zero":   0,
				"falsy":  false,
				"nulled": nil,
			},
			expected: map[string]interface{}{
				"empty": "",
				"zero":  0,
				"falsy": false,
			},
		},
		{
			name: "helm-like values with null operator",
			input: map[string]interface{}{
				"license": "XXXX",
				"global": map[string]interface{}{
					"operator": nil,
				},
			},
			expected: map[string]interface{}{
				"license": "XXXX",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Prune(tt.input)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("Prune() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestPruneDoesNotMutateInput(t *testing.T) {
	input := map[string]interface{}{
		"keep": "value",
		"drop": nil,
		"nested": map[string]interface{}{
			"a": 1,
			"b": nil,
		},
	}
	// Take a snapshot of the original
	original := map[string]interface{}{
		"keep": "value",
		"drop": nil,
		"nested": map[string]interface{}{
			"a": 1,
			"b": nil,
		},
	}

	_ = Prune(input)

	if !reflect.DeepEqual(input, original) {
		t.Errorf("Prune() mutated the input map: got %v, original was %v", input, original)
	}
}
