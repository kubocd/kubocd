/*
Copyright 2026 Kubotal

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tmpl

import (
	"reflect"
	"unicode/utf8"
)

const (
	saturatedBound  = maxRenderedText + 1
	diagnosticBound = 64
	floatBound      = 1024
	integerBound    = 64 + 3 + diagnosticBound
)

// valueSize summarizes a plain value without stringifying its contents.
type valueSize struct {
	size   int
	leaves int
}

// boundAdd adds nonnegative sizes, saturating before integer overflow.
func boundAdd(a, b int) int {
	if a > maxRenderedText || b > maxRenderedText-a {
		return saturatedBound
	}
	return a + b
}

// boundMultiply multiplies nonnegative sizes, saturating before integer overflow.
func boundMultiply(a, b int) int {
	if a == 0 || b == 0 {
		return 0
	}
	if a > maxRenderedText/b {
		return saturatedBound
	}
	return a * b
}

// plainSize bounds a plain operand, using general bounds for join and verb-aware bounds for printf.
func plainSize(value reflect.Value, d formatDirective) valueSize {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() && !d.general && d.sharp && (d.verb == 'v' || d.verb == 'w') {
			return valueSize{size: boundAdd(len(value.Type().String()), len("(nil)")), leaves: 1}
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return valueSize{size: nilSize(d), leaves: 1}
	}
	if !d.general && d.verb == 'T' {
		name := value.Type().String()
		return valueSize{size: stringSize(name, formatDirective{verb: 's', width: d.width, precision: d.precision, hasPrecision: d.hasPrecision}), leaves: 1}
	}
	if value.Type().NumMethod() != 0 {
		return valueSize{size: saturatedBound, leaves: 1}
	}
	switch value.Kind() {
	case reflect.String:
		return valueSize{size: stringSize(value.String(), d), leaves: 1}
	case reflect.Bool:
		return valueSize{size: boolSize(value, d), leaves: 1}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return numberSize(value.Kind(), d)
	case reflect.Slice, reflect.Array, reflect.Map:
		return containerSize(value, d)
	default:
		return valueSize{size: saturatedBound, leaves: 1}
	}
}

// nilSize counts fmt's nil representation, which ignores precision even for %T.
func nilSize(d formatDirective) int {
	if d.general {
		return len("%!(<nil>)") + utf8.UTFMax
	}
	if d.verb == 'v' || d.verb == 'T' {
		return min(saturatedBound, max(len("<nil>"), d.width))
	}
	// A bad verb on nil prints its diagnostic without padding.
	return len("%!(<nil>)") + utf8.RuneLen(d.verb)
}

// boolSize counts the boolean and any bad-verb envelope, with padding on the value only.
func boolSize(value reflect.Value, d formatDirective) int {
	size := len("false")
	if value.Bool() {
		size = len("true")
	}
	size = min(saturatedBound, max(size, d.width))
	if d.general || d.verb != 't' && d.verb != 'v' {
		verbSize := utf8.RuneLen(d.verb)
		if d.general {
			verbSize = utf8.UTFMax
		}
		envelope := boundAdd(len("%!(=)")+verbSize, len(value.Type().String()))
		size = boundAdd(size, envelope)
	}
	return size
}

// numberSize bounds numeric digits, per-component padding and diagnostic punctuation.
func numberSize(kind reflect.Kind, d formatDirective) valueSize {
	base := integerBound
	if kind == reflect.Float32 || kind == reflect.Float64 || kind == reflect.Complex64 || kind == reflect.Complex128 {
		base = floatBound
	}
	size := boundAdd(base, d.width)
	if !d.general && d.hasPrecision && numericPrecision(kind, d.verb) {
		size = boundAdd(size, d.precision)
	}
	if kind == reflect.Complex64 || kind == reflect.Complex128 {
		return valueSize{size: boundAdd(boundMultiply(2, size), len("(i)")+diagnosticBound), leaves: 2}
	}
	return valueSize{size: size, leaves: 1}
}

// numericPrecision identifies numeric representations whose digits can grow with precision.
func numericPrecision(kind reflect.Kind, verb rune) bool {
	switch kind {
	case reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	default:
		return verb != 'c' && verb != 'q'
	}
}

// stringSize counts plain strings exactly and bounds quoting, hexadecimal and diagnostic forms.
func stringSize(value string, d formatDirective) int {
	if d.general {
		return boundAdd(boundMultiply(5, len(value)), diagnosticBound)
	}
	hex := d.verb == 'x' || d.verb == 'X'
	if d.hasPrecision {
		if hex {
			value = value[:min(len(value), d.precision)]
		} else {
			value = stringPrefix(value, d.precision)
		}
	}
	quoted := d.verb == 'q' || d.sharp && (d.verb == 'v' || d.verb == 'w')
	if hex || quoted {
		size := boundAdd(boundMultiply(5, len(value)), diagnosticBound)
		return boundAdd(size, d.width)
	}
	size := boundAdd(len(value), max(0, d.width-utf8.RuneCountInString(value)))
	if d.verb != 's' && d.verb != 'v' {
		size = boundAdd(size, diagnosticBound)
	}
	return size
}

// stringPrefix selects fmt's precision in runes without allocating a substring copy.
func stringPrefix(value string, precision int) string {
	for i := range value {
		if precision == 0 {
			return value[:i]
		}
		precision--
	}
	return value
}

// containerSize sums element bounds without sorting maps or formatting their contents.
func containerSize(value reflect.Value, d formatDirective) valueSize {
	result := valueSize{size: diagnosticBound}
	add := func(child reflect.Value) {
		size := plainSize(child, d)
		result.size = boundAdd(result.size, boundAdd(size.size, 8))
		result.leaves = boundAdd(result.leaves, size.leaves)
	}
	if value.Kind() == reflect.Map {
		iter := value.MapRange()
		for iter.Next() {
			add(iter.Key())
			if result.size > maxRenderedText {
				break
			}
			add(iter.Value())
			if result.size > maxRenderedText {
				break
			}
		}
	} else {
		for i := 0; i < value.Len(); i++ {
			add(value.Index(i))
			if result.size > maxRenderedText {
				break
			}
		}
	}
	if result.leaves == 0 {
		result.leaves = 1
		result.size = boundAdd(result.size, d.width)
	}
	return result
}

// joinSize follows Sprig's item selection without constructing its string slice.
func joinSize(separator string, value interface{}) int {
	if value == nil {
		return 0
	}
	total, count := 0, 0
	add := func(item reflect.Value) {
		for item.IsValid() && item.Kind() == reflect.Interface {
			item = item.Elem()
		}
		if !item.IsValid() {
			return
		}
		if count > 0 {
			total = boundAdd(total, len(separator))
		}
		count++
		if item.Kind() == reflect.String && item.Type().NumMethod() == 0 {
			total = boundAdd(total, item.Len())
		} else {
			total = boundAdd(total, plainSize(item, formatDirective{general: true}).size)
		}
	}
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		for i := 0; i < v.Len(); i++ {
			add(v.Index(i))
			if total > maxRenderedText {
				break
			}
		}
	} else {
		add(v)
	}
	return total
}
