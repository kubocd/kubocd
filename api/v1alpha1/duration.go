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

package v1alpha1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Duration is a duration such as 90s or 2m30s, written as metav1.Duration is.
// Its decoding keeps a text that is not a duration, or is out of the range of
// time.Duration (about 2562047h), instead of failing: a client that lists the
// Releases decodes them all, and Validate reports the invalid one.
// +kubebuilder:validation:Type=string
type Duration struct {
	time.Duration

	// invalid holds the text that did not decode as a duration
	invalid *string
}

// NewDuration returns the Duration of d.
func NewDuration(d time.Duration) Duration {
	return Duration{Duration: d}
}

// UnmarshalJSON implements json.Unmarshaler. JSON null resets the Duration to
// zero. A string that is not a duration is kept, for Validate and MarshalJSON.
func (d *Duration) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*d = Duration{}
		return nil
	}
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		*d = Duration{invalid: &text}
		return nil
	}
	*d = Duration{Duration: parsed}
	return nil
}

// MarshalJSON implements json.Marshaler. An invalid Duration writes back the
// text it was decoded from, so that an update of the object keeps it.
func (d Duration) MarshalJSON() ([]byte, error) {
	if d.invalid != nil {
		return json.Marshal(*d.invalid)
	}
	return json.Marshal(d.String())
}

// ToUnstructured implements the value.UnstructuredConverter interface.
func (d Duration) ToUnstructured() interface{} {
	if d.invalid != nil {
		return *d.invalid
	}
	return d.String()
}

// Validate tells why the Duration cannot be used: a text that is not a
// duration, a duration out of range, or a negative one.
func (d Duration) Validate() error {
	if d.invalid != nil {
		return fmt.Errorf("%q is not a duration of at most %s, such as 90s or 2m30s",
			truncateInvalidDuration(*d.invalid), maxDuration)
	}
	if d.Duration < 0 {
		return fmt.Errorf("%q is a negative duration", d.String())
	}
	return nil
}

// MetaV1 returns the Duration as a metav1.Duration. An invalid one is zero.
func (d Duration) MetaV1() metav1.Duration {
	return metav1.Duration{Duration: d.Duration}
}

// MetaV1Ptr returns the Duration d points to as a metav1.Duration, and nil for a nil receiver.
func (d *Duration) MetaV1Ptr() *metav1.Duration {
	if d == nil {
		return nil
	}
	converted := d.MetaV1()
	return &converted
}

// maxDuration approximates the longest duration of time.Duration (math.MaxInt64 nanoseconds).
const maxDuration = "about 2562047h"

// maxInvalidDurationLen is the maximum byte length of an invalid duration quoted in Validate errors.
const maxInvalidDurationLen = 64

// truncateInvalidDuration cuts an oversized invalid duration text on a rune boundary so status messages and events stay bounded.
func truncateInvalidDuration(text string) string {
	if len(text) <= maxInvalidDurationLen {
		return text
	}
	const ellipsis = "..."
	cut := maxInvalidDurationLen - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.ToValidUTF8(text[:cut], "") + ellipsis
}

// ValidateDurations returns the first duration of the spec that cannot be used, with its path.
func (spec *ReleaseSpec) ValidateDurations() error {
	check := func(path string, d *Duration) error {
		if d == nil {
			return nil
		}
		if err := d.Validate(); err != nil {
			return fmt.Errorf("spec.%s: %w", path, err)
		}
		return nil
	}
	if err := check("package.interval", &spec.Package.Interval); err != nil {
		return err
	}
	if err := check("package.timeout", spec.Package.Timeout); err != nil {
		return err
	}
	for i := range spec.ModuleOverrides {
		override := &spec.ModuleOverrides[i]
		if err := check(fmt.Sprintf("moduleOverrides[%d].timeout", i), override.Timeout); err != nil {
			return err
		}
		if err := check(fmt.Sprintf("moduleOverrides[%d].interval", i), override.Interval); err != nil {
			return err
		}
	}
	return nil
}
