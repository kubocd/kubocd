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

import "unicode/utf8"

// hasPointerVerb reports whether format holds the %p verb. It walks format
// the way fmt.Sprintf does (fmt/print.go, doPrintf): flags, argument index,
// width, precision, argument index, verb. A divergence would let a %p through
// or reject a valid format, so the steps mirror fmt exactly.
func hasPointerVerb(format string) bool {
	end := len(format)
	for i := 0; i < end; {
		for i < end && format[i] != '%' {
			i++
		}
		if i >= end {
			break
		}
		i++
		for i < end && isFlag(format[i]) {
			i++
		}
		var afterIndex bool
		i, afterIndex = skipArgIndex(format, i)
		if i < end && format[i] == '*' {
			i++
			afterIndex = false
		} else {
			_, i = parseNumber(format, i, end)
		}
		if i+1 < end && format[i] == '.' {
			i++
			i, afterIndex = skipArgIndex(format, i)
			if i < end && format[i] == '*' {
				i++
				afterIndex = false
			} else {
				_, i = parseNumber(format, i, end)
			}
		}
		if !afterIndex {
			i, _ = skipArgIndex(format, i)
		}
		if i >= end {
			break
		}
		verb, size := utf8.DecodeRuneInString(format[i:])
		i += size
		if verb == 'p' {
			return true
		}
	}
	return false
}

func isFlag(c byte) bool {
	return c == '#' || c == '0' || c == '+' || c == '-' || c == ' '
}

// skipArgIndex skips an argument index such as [2] at format[i:], as fmt's
// argNumber does, and reports whether it was well formed.
func skipArgIndex(format string, i int) (int, bool) {
	if len(format) <= i || format[i] != '[' {
		return i, false
	}
	index := format[i:]
	if len(index) < 3 {
		return i + 1, false
	}
	for j := 1; j < len(index); j++ {
		if index[j] == ']' {
			isNumber, next := parseNumber(index, 1, j)
			return i + j + 1, isNumber && next == j
		}
	}
	return i + 1, false
}

// parseNumber is fmt's parsenum: it skips the decimal number at s[start:end],
// and jumps to end when the number is too large.
func parseNumber(s string, start, end int) (isNumber bool, next int) {
	if start >= end {
		return false, end
	}
	num := 0
	for next = start; next < end && '0' <= s[next] && s[next] <= '9'; next++ {
		if num > 1e6 || num < -1e6 {
			return false, end
		}
		num = num*10 + int(s[next]-'0')
		isNumber = true
	}
	return isNumber, next
}
