package misc

import (
	"fmt"
	"os"
)

type state int

const (
	STATE_NOMINAL state = iota
	STATE_AFTER_TOKEN
	STATE_IN_VAR
)

var _ error = &MissingVariableError{}

type MissingVariableError struct {
	line     int
	variable string
}

func (m MissingVariableError) Error() string {
	return fmt.Sprintf("environment variable '%s' at line %d is not defined", m.variable, m.line)
}

// ExpandEnv expand Environment variables in a string (Typically a configuration file).
// Difference from os.ExpandEnv:
// - Only ${..} vars are expanded. A single '$' is not taken in account. This will prevent a lot of side effect.
// - Missing variable will trigger an error
// NB: As os.ExpandEnv, we handle only ASCII characters
func ExpandEnv(s string) (string, error) {
	output := make([]byte, 0, len(s)+100)
	var variable []byte
	chunkStart := 0
	line := 1
	lenS := len(s)
	var state state = STATE_NOMINAL
	chunkEnd := -1
	for i := 0; i < lenS; i++ {
		if s[i] == '\n' {
			line++
		}
		switch state {
		case STATE_NOMINAL:
			if s[i] == '$' {
				state = STATE_AFTER_TOKEN
				chunkEnd = i
			}
		case STATE_AFTER_TOKEN:
			if s[i] == '{' {
				state = STATE_IN_VAR
				variable = make([]byte, 0, 20)
			} else {
				// It was a single $. Reject it
				state = STATE_NOMINAL
				chunkEnd = -1
			}
		case STATE_IN_VAR:
			if s[i] == '}' {
				// End of variable
				value := os.Getenv(string(variable))
				if value == "" {
					// Generate an error
					return "", MissingVariableError{
						line:     line,
						variable: string(variable),
					}
				}
				output = append(output, []byte(s[chunkStart:chunkEnd])...)
				output = append(output, []byte(value)...)
				chunkStart = i + 1
				chunkEnd = -1
			} else if isAlphanumeric(s[i]) {
				variable = append(variable, s[i])
			} else {
				// We must reject
				state = STATE_NOMINAL
				chunkEnd = -1
			}
		}
	}
	output = append(output, []byte(s[chunkStart:])...)

	return string(output), nil
}

func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}
