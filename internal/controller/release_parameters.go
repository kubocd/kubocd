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

package controller

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// The parts of a template action, as text/template lexes them.
const (
	actionCode = iota
	actionQuoted
	actionChar
	actionRaw
	actionComment
)

// jsonUnit is a character of a JSON string, as written, and the bytes that
// store it. A \u escape of a lone surrogate is invalid.
type jsonUnit struct {
	char    string
	raw     string
	invalid bool
}

// actionsAsWritten returns text, the JSON form of the parameters of a
// Release, with its template actions as their author wrote them, so that a
// quoted string, stored as \"...\", reads "...". From the {{ of an action to
// the }} that ends it, inside a JSON string, a JSON escape is decoded when it
// stands for a printable character other than a backslash, or for a blank
// between the words of the action, and a character written as it is must be
// one of those too. A comment keeps its stored bytes. The rest
// of the text is left as stored. The text is returned unchanged when an action
// holds another escape, such as \\ or \n in a string, whose value would differ
// from what renders back through the JSON string, or when an action does not
// end within its JSON string.
func actionsAsWritten(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		if text[i] != '"' {
			out.WriteByte(text[i])
			i++
			continue
		}
		units, next, ok := jsonStringUnits(text, i)
		if !ok {
			return text
		}
		written, ok := writtenActions(units)
		if !ok {
			return text
		}
		out.WriteByte('"')
		out.WriteString(written)
		out.WriteByte('"')
		i = next
	}
	return out.String()
}

// writtenActions returns the stored bytes of a JSON string, with its actions
// as actionsAsWritten tells, or false when one of them cannot be.
func writtenActions(units []jsonUnit) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(units); {
		if units[i].raw != "{" || i+1 == len(units) || units[i+1].raw != "{" {
			out.WriteString(units[i].raw)
			i++
			continue
		}
		action, next, ok := writtenAction(units, i)
		if !ok {
			return "", false
		}
		out.WriteString(action)
		i = next
	}
	return out.String(), true
}

// writtenAction returns the action whose {{ is units[start], as
// actionsAsWritten tells, and the index after its }}. It finds that end as
// text/template does. It tells false when the string ends first, or when the
// action holds an escape that it does not decode.
func writtenAction(units []jsonUnit, start int) (string, int, bool) {
	char := func(i int) string {
		if i < len(units) {
			return units[i].char
		}
		return ""
	}
	var out strings.Builder
	out.WriteString("{{")
	i := start + 2
	if char(i) == "-" && char(i+1) != "" && strings.Contains(" \t\r\n", char(i+1)) {
		if !decodable(units[i+1], actionCode) {
			return "", 0, false
		}
		out.WriteString(char(i) + char(i+1))
		i += 2
	}
	state := actionCode
	// A comment is one only right after the delimiter and its trim marker
	if char(i) == "/" && char(i+1) == "*" {
		out.WriteString("/*")
		i += 2
		state = actionComment
	}
	for ; i < len(units); i++ {
		c := units[i].char
		if state == actionComment {
			if c == "*" && char(i+1) == "/" {
				out.WriteString("*/")
				i++
				state = actionCode
				continue
			}
			out.WriteString(units[i].raw)
			continue
		}
		if !decodable(units[i], state) {
			return "", 0, false
		}
		switch {
		case state != actionCode:
			if c == `"` && state == actionQuoted || c == "'" && state == actionChar || c == "`" && state == actionRaw {
				state = actionCode
			}
		case c == "}" && char(i+1) == "}":
			out.WriteString("}}")
			return out.String(), i + 2, true
		default:
			state = codeState(c)
		}
		out.WriteString(c)
	}
	return "", 0, false
}

// codeState returns the part of an action that the character c opens, in its
// code.
func codeState(c string) int {
	switch c {
	case `"`:
		return actionQuoted
	case "'":
		return actionChar
	case "`":
		return actionRaw
	}
	return actionCode
}

// decodable tells whether u, in the given part of an action, can be read as
// its character, written or escaped: that character is printable and not a
// backslash, or it is a blank in the code.
func decodable(u jsonUnit, state int) bool {
	r, size := utf8.DecodeRuneInString(u.char)
	if u.invalid || r == utf8.RuneError && size <= 1 {
		return false
	}
	switch r {
	case '\\':
		return false
	case '\t', '\n', '\r':
		return state == actionCode
	}
	return unicode.IsGraphic(r)
}

// jsonStringUnits returns the characters of the JSON string whose opening
// quote is text[start], and the index after its closing quote.
func jsonStringUnits(text string, start int) ([]jsonUnit, int, bool) {
	units := make([]jsonUnit, 0)
	for i := start + 1; i < len(text); {
		switch text[i] {
		case '"':
			return units, i + 1, true
		case '\\':
			char, n, invalid := jsonEscape(text, i)
			if n == 0 {
				return nil, 0, false
			}
			units = append(units, jsonUnit{char: char, raw: text[i : i+n], invalid: invalid})
			i += n
		default:
			_, size := utf8.DecodeRuneInString(text[i:])
			units = append(units, jsonUnit{char: text[i : i+size], raw: text[i : i+size]})
			i += size
		}
	}
	return nil, 0, false
}

// jsonEscape decodes the JSON escape at text[i], and returns the character,
// the length of the escape, 0 if it is not one, and whether it is the escape
// of a lone surrogate, decoded as U+FFFD.
func jsonEscape(text string, i int) (string, int, bool) {
	if i+1 >= len(text) {
		return "", 0, false
	}
	switch text[i+1] {
	case '"', '\\', '/':
		return text[i+1 : i+2], 2, false
	case 'b':
		return "\b", 2, false
	case 'f':
		return "\f", 2, false
	case 'n':
		return "\n", 2, false
	case 'r':
		return "\r", 2, false
	case 't':
		return "\t", 2, false
	case 'u':
		r, ok := jsonHex(text, i)
		if !ok {
			return "", 0, false
		}
		if !utf16.IsSurrogate(r) {
			return string(r), 6, false
		}
		if low, ok := jsonHex(text, i+6); ok {
			if pair := utf16.DecodeRune(r, low); pair != utf8.RuneError {
				return string(pair), 12, false
			}
		}
		return string(utf8.RuneError), 6, true
	}
	return "", 0, false
}

// jsonHex reads the 4 hexadecimal digits of the \u escape at text[i].
func jsonHex(text string, i int) (rune, bool) {
	if i+6 > len(text) || text[i] != '\\' || text[i+1] != 'u' {
		return 0, false
	}
	value, err := strconv.ParseUint(text[i+2:i+6], 16, 16)
	if err != nil {
		return 0, false
	}
	return rune(value), true
}
