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

package cmd

import (
	"kubocd/internal/tmpl"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const chartControllerComponent = "controller"

func TestAllowDNSLookupsIsOffByDefault(t *testing.T) {
	for _, command := range []*cobra.Command{controllerCmd, renderCmd} {
		flag := command.PersistentFlags().Lookup("allowDNSLookups")
		if flag == nil || flag.DefValue != "false" {
			t.Errorf("%s: got flag %v, want --allowDNSLookups off by default", command.Name(), flag)
		}
	}
}

func TestRenderGivesItsDNSPolicyToPackageTemplates(t *testing.T) {
	t.Cleanup(func() { renderParams.allowDNSLookups = false })
	for _, args := range [][]string{nil, {"--allowDNSLookups"}} {
		renderParams.allowDNSLookups = false
		if err := renderCmd.PersistentFlags().Parse(args); err != nil {
			t.Fatal(err)
		}
		tt, err := tmpl.New("", `{{ resolveDNS "127.0.0.1" }}`, "", renderPackageFunctions())
		if err != nil {
			t.Fatal(err)
		}
		got, err := tt.RenderToText(map[string]interface{}{})
		if args == nil {
			if want := "resolveDNS is disabled by platform policy"; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("without the flag: got %q (%v), want an error containing %q", got, err, want)
			}
		} else if err != nil || got != "127.0.0.1" {
			t.Errorf("with the flag: got %q (%v), want 127.0.0.1", got, err)
		}
	}
}

// controllerArgs renders the Deployment of the kubocd-ctrl chart, its values
// changed by setValues, and returns the arguments of the controller.
func controllerArgs(t *testing.T, setValues func(controller map[string]interface{})) []string {
	t.Helper()
	deployment := chartDeployment(t, "kubocd-ctrl", func(values map[string]interface{}) {
		setValues(values[chartControllerComponent].(map[string]interface{}))
	})
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == chartControllerComponent {
			return container.Args
		}
	}
	t.Fatal("no controller container in the Deployment")
	return nil
}

func TestChartGivesItsDNSPolicyToTheController(t *testing.T) {
	saved := controllerParams
	t.Cleanup(func() { controllerParams = saved })
	cases := map[string]struct {
		setValues func(controller map[string]interface{})
		want      bool
	}{
		"chart default": {func(map[string]interface{}) {}, false},
		"value absent":  {func(controller map[string]interface{}) { delete(controller, "allowDNSLookups") }, false},
		"value false":   {func(controller map[string]interface{}) { controller["allowDNSLookups"] = false }, false},
		"value true":    {func(controller map[string]interface{}) { controller["allowDNSLookups"] = true }, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			args := controllerArgs(t, c.setValues)
			if got := slices.Contains(args, "--allowDNSLookups=true"); got != c.want {
				t.Fatalf("got arguments %v, want --allowDNSLookups=true: %v", args, c.want)
			}
			controllerParams.allowDNSLookups = false
			if err := controllerCmd.PersistentFlags().Parse(args[1:]); err != nil {
				t.Fatal(err)
			}
			if controllerParams.allowDNSLookups != c.want {
				t.Fatalf("the controller reads allowDNSLookups %v, want %v", controllerParams.allowDNSLookups, c.want)
			}
		})
	}
}
