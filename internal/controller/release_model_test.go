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
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"slices"
	"testing"
)

// escapeOf returns the JSON escape of a UTF-16 code unit. It is built from
// parts: a tool can turn an escaped surrogate pair written as such into its
// character.
func escapeOf(unit string) string {
	return `\` + "u" + unit
}

func TestAReleaseThatDoesNotConvertToYAMLGivesAnError(t *testing.T) {
	cases := map[string]string{
		"lone surrogate": `{"replicas":2,"v":"` + escapeOf("d800") + `"}`,
		"surrogate pair": `{"replicas":2,"v":"` + escapeOf("d83d") + escapeOf("de00") + `"}`,
	}
	for name, parameters := range cases {
		t.Run(name, func(t *testing.T) {
			release := newTestRelease("demo", parameters)
			if _, err := BuildModel(map[string]interface{}{}, nil, release, newTestStore(t)); !errors.Is(err, errReleaseModel) {
				t.Fatalf("BuildModel: got the error %v, want %q", err, errReleaseModel)
			}
			testbed := newReleaseTestbed(t, fmt.Sprintf(reconciledPackage, "replicaCount: 1"), parameters, "")
			got := testbed.reconcile(t)
			if phase := phaseOf(t, testbed); got.err != nil || phase != kv1alpha1.ReleasePhaseError || got.status != errReleaseModel.Error() {
				t.Fatalf("got the phase %q, the status %q and the error %v, want ERROR with %q", phase, got.status, got.err,
					errReleaseModel)
			}
			// The model is first built for the parameters, whose errors have this reason
			if want := "Warning Parameters " + errReleaseModel.Error(); !slices.Contains(got.events, want) {
				t.Fatalf("got the events %q, want %q", got.events, want)
			}
		})
	}
}

func TestAReleaseWithACharacterOutsideTheBasicPlaneConvertsToYAML(t *testing.T) {
	release := newTestRelease("demo", `{"v":"`+string(rune(0x1F600))+`"}`)
	model, err := BuildModel(map[string]interface{}{}, nil, release, newTestStore(t))
	if err != nil {
		t.Fatal(err)
	}
	spec := model["Release"].(map[string]interface{})["spec"].(map[string]interface{})
	if got := spec["parameters"].(map[string]interface{})["v"]; got != string(rune(0x1F600)) {
		t.Fatalf("got the parameter %q, want U+1F600", got)
	}
}
