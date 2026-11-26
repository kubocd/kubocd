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

//nolint:goconst // test inputs read better inline
package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestADurationDecodesEveryStringAndValidateTellsTheInvalidOnes(t *testing.T) {
	cases := map[string]struct {
		text    string
		want    time.Duration
		invalid string
	}{
		"valid":                           {"2m30s", 150 * time.Second, ""},
		"the longest":                     {"2562047h", 2562047 * time.Hour, ""},
		"beyond the range of a duration":  {"9999999h", 0, `"9999999h" is not a duration of at most about 2562047h`},
		"beyond the range, in minutes":    {"9999999999m", 0, `"9999999999m" is not a duration`},
		"not a duration":                  {"garbage", 0, `"garbage" is not a duration`},
		"empty":                           {"", 0, `"" is not a duration`},
		"negative":                        {"-5m", -5 * time.Minute, `"-5m0s" is a negative duration`},
		"zero, which the defaults handle": {"0s", 0, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var d Duration
			raw, _ := json.Marshal(c.text)
			if err := json.Unmarshal(raw, &d); err != nil {
				t.Fatalf("got the error %v, want the text kept", err)
			}
			if d.Duration != c.want {
				t.Errorf("got %v, want %v", d.Duration, c.want)
			}
			err := d.Validate()
			if c.invalid == "" && err != nil || c.invalid != "" && (err == nil || !strings.Contains(err.Error(), c.invalid)) {
				t.Errorf("got the error %v, want %q", err, c.invalid)
			}
		})
	}
}

func TestADurationThatIsNotAStringIsStillAnError(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`12`), &d); err == nil {
		t.Fatal("got no error, want the one of a number where a string is expected")
	}
}

func TestAnInvalidDurationIsWrittenBackAsItWasRead(t *testing.T) {
	spec := `{"package":{"repository":"r","tag":"t","interval":"9999999h","timeout":"1m"},` +
		`"moduleOverrides":[{"module":"main","timeout":"garbage","interval":"5m"}],"createNamespace":false}`
	release := &Release{}
	if err := json.Unmarshal([]byte(`{"spec":`+spec+`}`), release); err != nil {
		t.Fatal(err)
	}
	for name, encode := range map[string]func(*Release) (map[string]interface{}, error){
		"json": func(r *Release) (map[string]interface{}, error) {
			raw, err := json.Marshal(r)
			if err != nil {
				return nil, err
			}
			object := map[string]interface{}{}
			return object, json.Unmarshal(raw, &object)
		},
		"unstructured": func(r *Release) (map[string]interface{}, error) {
			return runtime.DefaultUnstructuredConverter.ToUnstructured(r)
		},
		"deep copy": func(r *Release) (map[string]interface{}, error) {
			return runtime.DefaultUnstructuredConverter.ToUnstructured(r.DeepCopy())
		},
	} {
		object, err := encode(release)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		pck := object["spec"].(map[string]interface{})["package"].(map[string]interface{})
		override := object["spec"].(map[string]interface{})["moduleOverrides"].([]interface{})[0].(map[string]interface{})
		if pck["interval"] != "9999999h" || pck["timeout"] != "1m0s" || override["timeout"] != "garbage" || override["interval"] != "5m0s" {
			t.Errorf("%s: got %v and %v, want the texts read", name, pck, override)
		}
	}
	decoded := &Release{}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object, decoded); err != nil {
		t.Fatalf("got %v, want the unstructured Release decoded", err)
	}
	if err := decoded.Spec.ValidateDurations(); err == nil || err.Error() != `spec.package.interval: "9999999h" is not a duration of at most about 2562047h, such as 90s or 2m30s` {
		t.Fatalf("got %v, want the invalid interval", err)
	}
}

func TestADurationDecodesJSONNullAsZero(t *testing.T) {
	bad := "garbage"
	for name, initial := range map[string]Duration{
		"zero":    {},
		"valid":   NewDuration(5 * time.Minute),
		"invalid": {invalid: &bad},
	} {
		t.Run(name, func(t *testing.T) {
			d := initial
			if err := json.Unmarshal([]byte(" null "), &d); err != nil {
				t.Fatalf("got %v, want nil error on JSON null", err)
			}
			if d.Duration != 0 || d.invalid != nil {
				t.Fatalf("got %+v, want zero Duration", d)
			}
			if err := d.Validate(); err != nil {
				t.Fatalf("got %v, want zero Duration valid", err)
			}
		})
	}
	release := &Release{}
	raw := `{"spec":{"package":{"repository":"r","tag":"t","interval":null,"timeout":null}}}`
	if err := json.Unmarshal([]byte(raw), release); err != nil {
		t.Fatalf("got %v, want Release with null durations decoded", err)
	}
	if release.Spec.Package.Interval != (Duration{}) || release.Spec.Package.Timeout != nil {
		t.Fatalf("got interval=%+v timeout=%+v, want zero and nil", release.Spec.Package.Interval, release.Spec.Package.Timeout)
	}
	if err := release.Spec.ValidateDurations(); err != nil {
		t.Fatalf("got %v, want valid durations", err)
	}
}

func TestAnInvalidDurationIsTruncatedInValidateWhileKeepingRawText(t *testing.T) {
	for name, text := range map[string]string{
		"ascii":     strings.Repeat("a", 200),
		"multibyte": strings.Repeat("é", 40),
	} {
		t.Run(name, func(t *testing.T) {
			var d Duration
			raw, err := json.Marshal(text)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &d); err != nil {
				t.Fatal(err)
			}
			err = d.Validate()
			if err == nil {
				t.Fatal("got nil error, want validation error")
			}
			wantQuoted := truncateInvalidDuration(text)
			if len(wantQuoted) > maxInvalidDurationLen || !strings.HasSuffix(wantQuoted, "...") || !utf8.ValidString(wantQuoted) {
				t.Fatalf("truncated text %q violates bounds or UTF-8", wantQuoted)
			}
			if !strings.Contains(err.Error(), wantQuoted) || strings.Contains(err.Error(), text) {
				t.Fatalf("got error %q, want truncated %q without full input", err.Error(), wantQuoted)
			}
			marshaled, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if string(marshaled) != string(raw) {
				t.Fatalf("got marshaled %s, want full original %s", marshaled, raw)
			}
		})
	}
}

func TestDurationMetaV1PtrIsNilSafe(t *testing.T) {
	var nilDuration *Duration
	if got := nilDuration.MetaV1Ptr(); got != nil {
		t.Fatalf("got %v, want nil for nil receiver", got)
	}
	d := NewDuration(90 * time.Second)
	got := d.MetaV1Ptr()
	if got == nil || got.Duration != 90*time.Second {
		t.Fatalf("got %v, want 90s", got)
	}
}

func TestValidateDurationsNamesTheField(t *testing.T) {
	invalid := func(text string) *Duration {
		d := &Duration{}
		raw, _ := json.Marshal(text)
		_ = json.Unmarshal(raw, d)
		return d
	}
	cases := map[string]struct {
		spec ReleaseSpec
		want string
	}{
		"valid": {ReleaseSpec{Package: PackageSourceSpec{Interval: NewDuration(time.Minute), Timeout: new(NewDuration(time.Minute))},
			ModuleOverrides: []ModuleOverrideSpec{{Module: "main", Timeout: new(NewDuration(time.Minute))}}}, ""},
		"package interval": {ReleaseSpec{Package: PackageSourceSpec{Interval: *invalid("9999999h")}}, "spec.package.interval: "},
		"package timeout":  {ReleaseSpec{Package: PackageSourceSpec{Timeout: invalid("9999999999m")}}, "spec.package.timeout: "},
		"override timeout": {ReleaseSpec{ModuleOverrides: []ModuleOverrideSpec{{Module: "a"}, {Module: "b", Timeout: invalid("9999999h")}}},
			"spec.moduleOverrides[1].timeout: "},
		"override interval": {ReleaseSpec{ModuleOverrides: []ModuleOverrideSpec{{Module: "a", Interval: invalid("-1s")}}},
			"spec.moduleOverrides[0].interval: "},
	}
	for name, c := range cases {
		err := c.spec.ValidateDurations()
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.HasPrefix(err.Error(), c.want)) {
			t.Errorf("%s: got %v, want an error starting with %q", name, err, c.want)
		}
	}
}
