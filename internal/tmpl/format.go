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

// formatDirective carries the controls fmt applies to an operand.
type formatDirective struct {
	verb         rune
	width        int
	precision    int
	hasPrecision bool
	sharp        bool
	general      bool
}

// formatWalk tracks fmt's argument consumption and the bound of its output.
type formatWalk struct {
	format    string
	args      []interface{}
	next      int
	reordered bool
	size      int
	pointer   bool
}

// hasPointerVerb reports whether the shared fmt directive walk encounters %p.
func hasPointerVerb(format string) bool {
	_, pointer := printfSize(format, nil)
	return pointer
}

// printfSize bounds formatting while walking the same directives as fmt.Sprintf.
func printfSize(format string, args []interface{}) (int, bool) {
	w := formatWalk{format: format, args: args}
	for i := 0; i < len(format); {
		start := i
		for i < len(format) && format[i] != '%' {
			i++
		}
		w.size = boundAdd(w.size, i-start)
		if i < len(format) {
			i = w.directive(i + 1)
		}
	}
	if !w.reordered && w.next < len(args) && w.size <= maxRenderedText {
		w.extra()
	}
	return w.size, w.pointer
}

// directive must follow fmt/print.go doPrintf: flags, argument index, width, precision, argument index, verb.
// A divergence can let %p through or reject a valid format.
func (w *formatWalk) directive(i int) int {
	d := formatDirective{}
	for i < len(w.format) && isFlag(w.format[i]) {
		d.sharp = d.sharp || w.format[i] == '#'
		i++
	}
	good := true
	i, afterIndex := w.argIndex(i, &good)
	if i < len(w.format) && w.format[i] == '*' {
		d.width = w.star(false)
		i++
		afterIndex = false
	} else {
		var present bool
		d.width, present, i = parseNumber(w.format, i, len(w.format))
		if afterIndex && present {
			good = false
		}
	}
	if i+1 < len(w.format) && w.format[i] == '.' {
		if afterIndex {
			good = false
		}
		i, afterIndex = w.argIndex(i+1, &good)
		d.hasPrecision = true
		if i < len(w.format) && w.format[i] == '*' {
			d.precision = w.star(true)
			d.hasPrecision = d.precision >= 0
			i++
			afterIndex = false
		} else {
			d.precision, _, i = parseNumber(w.format, i, len(w.format))
		}
	}
	if !afterIndex {
		i, _ = w.argIndex(i, &good)
	}
	if i >= len(w.format) {
		w.size = boundAdd(w.size, len("%!(NOVERB)"))
		return i
	}
	verb, size := utf8.DecodeRuneInString(w.format[i:])
	d.verb = verb
	w.pointer = w.pointer || verb == 'p'
	switch {
	case verb == '%':
		w.size = boundAdd(w.size, 1)
	case !good || w.next >= len(w.args):
		w.size = boundAdd(w.size, diagnosticBound)
	default:
		if w.size <= maxRenderedText {
			value := plainSize(reflect.ValueOf(w.args[w.next]), d)
			w.size = boundAdd(w.size, value.size)
		}
		w.next++
	}
	return i + size
}

// argIndex mirrors fmt's argNumber, including malformed indexes suppressing EXTRA.
func (w *formatWalk) argIndex(i int, good *bool) (int, bool) {
	if i >= len(w.format) || w.format[i] != '[' {
		return i, false
	}
	w.reordered = true
	index, next, valid := parseArgIndex(w.format, i)
	if valid && index > 0 && index <= len(w.args) {
		w.next = index - 1
	} else {
		*good = false
	}
	return next, valid
}

// parseArgIndex reads an argument index with fmt's recovery for missing brackets.
func parseArgIndex(format string, i int) (int, int, bool) {
	if len(format)-i >= 3 {
		for j := i + 1; j < len(format); j++ {
			if format[j] == ']' {
				number, present, next := parseNumber(format, i+1, j)
				return number, j + 1, present && next == j
			}
		}
	}
	return 0, i + 1, false
}

// star consumes a dynamic control, checking its range before taking an absolute value.
func (w *formatWalk) star(precision bool) int {
	value, valid := 0, false
	if w.next < len(w.args) {
		arg := reflect.ValueOf(w.args[w.next])
		w.next++
		if arg.IsValid() {
			switch arg.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				n := arg.Int()
				if n >= -1000000 && n <= 1000000 {
					value, valid = int(n), true
				}
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
				n := arg.Uint()
				if n <= 1000000 {
					value, valid = int(n), true
				}
			}
		}
	}
	if precision && value < 0 {
		valid = false
	}
	if !valid {
		if precision {
			w.size = boundAdd(w.size, len("%!(BADPREC)"))
			return -1
		}
		w.size = boundAdd(w.size, len("%!(BADWIDTH)"))
		return 0
	}
	if value < 0 {
		return -value
	}
	return value
}

// extra counts only the remaining arguments that fmt includes in an EXTRA diagnostic.
func (w *formatWalk) extra() {
	w.size = boundAdd(w.size, len("%!(EXTRA ")+1)
	for i, arg := range w.args[w.next:] {
		if i > 0 {
			w.size = boundAdd(w.size, 2)
		}
		if arg == nil {
			w.size = boundAdd(w.size, len("<nil>"))
		} else {
			w.size = boundAdd(w.size, len(reflect.TypeOf(arg).String())+1)
			value := plainSize(reflect.ValueOf(arg), formatDirective{verb: 'v'})
			w.size = boundAdd(w.size, value.size)
		}
		if w.size > maxRenderedText {
			return
		}
	}
}

// isFlag reports fmt's formatting flags.
func isFlag(c byte) bool {
	return c == '#' || c == '0' || c == '+' || c == '-' || c == ' '
}

// parseNumber is fmt's parsenum, including its check before consuming the next digit.
func parseNumber(s string, start, end int) (number int, present bool, next int) {
	for next = start; next < end && '0' <= s[next] && s[next] <= '9'; next++ {
		if number > 1e6 {
			return 0, false, end
		}
		number = number*10 + int(s[next]-'0')
		present = true
	}
	return number, present, next
}
