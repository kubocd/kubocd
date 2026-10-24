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
package controller

import (
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/cache"
	"kubocd/internal/global"
	"kubocd/internal/rolestore"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// reconciledRelease is what one reconciliation of a Release left behind.
type reconciledRelease struct {
	result ctrl.Result
	err    error
	status string
	events []string
	log    string
}

// sinks returns everything a user or an operator can read about the
// reconciliation: the returned error, logged by controller-runtime, the
// status message, the events and the log.
func (r reconciledRelease) sinks() map[string]string {
	sinks := map[string]string{"status": r.status, "events": strings.Join(r.events, "\n"), "log": r.log}
	if r.err != nil {
		sinks["returned error"] = r.err.Error()
	}
	return sinks
}

type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *logBuffer) write(prefix, args string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.WriteString(prefix + " " + args + "\n")
}

const reconcileRevision = "0.1.0@sha256:0123456789abcdef0123456789abcdef"

// releaseTestbed is a fake API server that holds the Release demo, and the
// reconciler of that Release.
type releaseTestbed struct {
	client     client.WithWatch
	reconciler *ReleaseReconciler
	recorder   *record.FakeRecorder
	log        *logBuffer
}

// newReleaseTestbed prepares the reconciliations of the Release demo, whose
// package manifest is already fetched, against a fake API server. A non empty
// contextValues is the Context the Release uses. The module main has a chart,
// the REST mapper knows the Context kind, and the fake API server serves the
// indexes of the output connections of a Release.
func newReleaseTestbed(t *testing.T, manifest string, parameters string, contextValues string, options ...func(*ReleaseReconciler)) *releaseTestbed {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{kv1alpha1.AddToScheme, sourcev1.AddToScheme, fluxv2.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ready := []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", LastTransitionTime: metav1.Now()}}
	artifact := &meta.Artifact{Revision: reconcileRevision, Digest: "sha256:0123", Path: "demo.tgz", URL: "http://source/demo.tgz", LastUpdateTime: metav1.Now()}
	release := newTestRelease("demo", parameters)
	release.UID = "demo"
	release.Finalizers = []string{global.FinalizerName}
	objects := []client.Object{
		&sourcev1.OCIRepository{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf(OciRepositoryNameFormat, "demo"), Namespace: "apps"},
			Status:     sourcev1.OCIRepositoryStatus{Conditions: ready, Artifact: artifact},
		},
		&sourcev1.HelmRepository{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf(HelmRepositoryNameFormat, "demo"), Namespace: "apps",
				Annotations: map[string]string{packageRevisionAnnotation: reconcileRevision}},
			Status: sourcev1.HelmRepositoryStatus{Conditions: ready, Artifact: artifact},
		},
	}
	if contextValues != "" {
		release.Spec.Contexts = []kv1alpha1.NamespacedName{{Namespace: "apps", Name: "cluster"}}
		objects = append(objects, &kv1alpha1.Context{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "apps"},
			Spec:       kv1alpha1.ContextSpec{Context: &apiextensionsv1.JSON{Raw: []byte(contextValues)}},
			Status:     kv1alpha1.ContextStatus{Phase: kv1alpha1.ContextPhaseReady},
		})
	}
	restMapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{kv1alpha1.GroupVersion})
	restMapper.Add(kv1alpha1.GroupVersion.WithKind("Context"), apimeta.RESTScopeNamespace)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(restMapper).WithObjects(append(objects, release)...).
		WithStatusSubresource(&kv1alpha1.Release{}, &kv1alpha1.Connection{}, &kv1alpha1.ClusterConnection{}).
		WithIndex(&kv1alpha1.Connection{}, ReleaseIndexOnOutputConnection, func(o client.Object) []string {
			return OutputConnectionReleaseKeys(o.(*kv1alpha1.Connection))
		}).
		WithIndex(&kv1alpha1.ClusterConnection{}, ReleaseIndexOnOutputClusterConnection, func(o client.Object) []string {
			return OutputClusterConnectionReleaseKeys(o.(*kv1alpha1.ClusterConnection))
		}).Build()

	serverRoot := t.TempDir()
	folder := filepath.Join(serverRoot, "hr", "apps", "demo")
	files := map[string]string{"revision.txt": reconcileRevision, "original.yaml": manifest, "status.yaml": "apiVersion: v1alpha1\nchartByModule:\n  main: {name: demo, version: 1.0.0}\n"}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	log := &logBuffer{}
	store := newTestStore(t)
	recorder := record.NewFakeRecorder(100)
	reconciler := &ReleaseReconciler{
		Client:          k8sClient,
		EventRecorder:   recorder,
		Logger:          funcr.New(log.write, funcr.Options{Verbosity: 2}),
		ServerRoot:      serverRoot,
		HelmRepoAdvAddr: "kubocd-helm-repository.kubocd.svc",
		Fetcher:         NewPackageFetcher(DefaultPackageLimits, 0, ""),
		PackageCache:    cache.NewCache(time.Minute, logr.Discard()),
		ConfigStore:     store,
		RoleStore:       rolestore.New(store, logr.Discard()),
	}
	for _, option := range options {
		option(reconciler)
	}
	return &releaseTestbed{client: k8sClient, reconciler: reconciler, recorder: recorder, log: log}
}

// reconcile reconciles the Release once. The log holds every reconciliation
// done so far, the events this one only.
func (b *releaseTestbed) reconcile(t *testing.T) reconciledRelease {
	t.Helper()
	key := types.NamespacedName{Namespace: "apps", Name: "demo"}
	reconciled, err := b.reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	got := &kv1alpha1.Release{}
	if err := b.client.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	result := reconciledRelease{result: reconciled, err: err, status: got.Status.Message, log: b.log.buf.String()}
	for len(b.recorder.Events) > 0 {
		result.events = append(result.events, <-b.recorder.Events)
	}
	return result
}

// reconcileRelease reconciles once the Release demo of newReleaseTestbed.
func reconcileRelease(t *testing.T, manifest string, parameters string, contextValues string, options ...func(*ReleaseReconciler)) reconciledRelease {
	t.Helper()
	return newReleaseTestbed(t, manifest, parameters, contextValues, options...).reconcile(t)
}

// reconciledPackage has a parameter schema and the values given.
const reconciledPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
schema:
  parameters:
    properties:
      replicas:
        type: integer
        default: 1
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
    values: |
      %s
`

const canaryContext = `{"db":{"host":"pg","password":"` + canaryPassword + `("},"other":{"password":"other"},` +
	`"fake":"yaml: line 731924: ` + canaryPassword + `"}`

func TestRenderErrorsStayOutOfStatusEventsAndLog(t *testing.T) {
	cases := map[string]struct {
		values     string
		parameters string
		want       string
	}{
		"range over a value": {"hosts: {{ range .Context.db.password }}{{ end }}", "",
			"could not render 'values' template: line 1, column 25: range over a value that is not a list or a map, message withheld"},
		"comparison": {"same: {{ eq .Context.other .Context.db }}", "",
			"could not render 'values' template: line 1, column 10: error calling eq, message withheld"},
		"invalid pattern, recovered panic": {`name: {{ regexReplaceAll .Context.db.password "x" "y" }}`, "",
			"could not render 'values' template: line 1, column 10: error calling regexReplaceAll, message withheld"},
		"line number in the data": {`port: !!int "{{ .Context.fake }}"`, "",
			"could not render 'values' template: the rendered text is not valid YAML"},
		"undeclared parameter": {"replicas: 1", `{"{{ .Context.db.password }}":1}`,
			"could not validate parameters: parameters schema validation error: (root): a property is not declared in the schema"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := reconcileRelease(t, fmt.Sprintf(reconciledPackage, c.values), c.parameters, canaryContext)
			got.mustReport(t, c.want)
			for sink, text := range got.sinks() {
				if strings.Contains(text, canaryPassword[:7]) || strings.Contains(text, "731924") {
					t.Errorf("the %s quotes the data:\n%s", sink, text)
				}
			}
		})
	}
}

// mustReport fails the test unless the status, an event and the log hold
// want.
func (r reconciledRelease) mustReport(t *testing.T, want string) {
	t.Helper()
	if !strings.Contains(r.status, want) {
		t.Fatalf("got status %q, want one containing %q", r.status, want)
	}
	if !slices.ContainsFunc(r.events, func(event string) bool { return strings.Contains(event, want) }) {
		t.Fatalf("got events %q, want one containing %q", r.events, want)
	}
	if !strings.Contains(r.log, want) {
		t.Fatalf("got log\n%s\nwant one containing %q", r.log, want)
	}
}

// connectionRefPackage takes the name of its database connection from the
// Release parameters, or from a templated default.
const connectionRefPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
schema:
  parameters:
    properties:
      db:
        type: connectionRef
        contract: database
        default: '{{ .Context.dbName }}'
      replicas:
        type: integer
        default: 1
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
    values: |
      db: {{ .Parameters.db.host }}
`

// canaryName would be quoted by the template parser as a function name.
const canaryName = "S3cr3tSEC1CanaryName"

func TestConnectionNameSetByTheReleaseIsNotParsed(t *testing.T) {
	contextValues := `{"dbName":"pg","db":{"name":"{{ ` + canaryName + ` }}"}}`
	cases := map[string]struct {
		parameters string
		want       string
	}{
		"name set by the Release, from the Context": {`{"db":"{{ .Context.db.name }}","replicas":"many"}`,
			"could not validate parameters: parameters schema validation error: replicas: Invalid type. Expected: integer, given: string"},
		"templated default": {"", "input#1: Waiting for connection 'apps:pg'"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := reconcileRelease(t, connectionRefPackage, c.parameters, contextValues)
			for sink, text := range got.sinks() {
				if strings.Contains(text, canaryName) || strings.Contains(text, "not defined") {
					t.Errorf("the %s quotes the data:\n%s", sink, text)
				}
			}
			got.mustReport(t, c.want)
		})
	}
}

func TestAuthorMessagesReachTheStatus(t *testing.T) {
	got := reconcileRelease(t, fmt.Sprintf(reconciledPackage, `{{ fail (printf "no database on %s" .Context.db.host) }}`), "", canaryContext)
	if want := "error calling fail: no database on pg"; !strings.Contains(got.status, want) {
		t.Fatalf("got status %q, want one containing %q", got.status, want)
	}
}

func TestARenderPastALimitIsReportedAndNotRetried(t *testing.T) {
	cases := map[string]struct{ values, want string }{
		"rendered text": {`big: "{{ range 5 }}{{ printf "%1000000s" "" }}{{ end }}"`,
			"could not render 'values' template: the rendered text is larger than 4194304 bytes"},
		"until": {"items: {{ until 10001 | len }}",
			"could not render 'values' template: line 1, column 11: error calling until: until: the count is beyond the limit of 10000"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := reconcileRelease(t, fmt.Sprintf(reconciledPackage, c.values), "", canaryContext)
			want := "error on rendering: module 'main': " + c.want
			if got.status != want || got.err != nil || !got.result.IsZero() {
				t.Fatalf("got the status %q, the error %v and the result %+v, want the status %q, no error and no requeue",
					got.status, got.err, got.result, want)
			}
		})
	}
	// A control: a render error on no limit is still returned, for a retry
	got := reconcileRelease(t, fmt.Sprintf(reconciledPackage, `{{ fail "no database" }}`), "", canaryContext)
	if got.err == nil {
		t.Fatalf("got no error and the status %q, want the error returned", got.status)
	}
}

func allowDNSLookups(allow bool) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) { r.AllowDNSLookups = allow }
}

func TestReconcileGivesItsDNSPolicyToPackageTemplates(t *testing.T) {
	manifest := fmt.Sprintf(reconciledPackage, `ip: {{ resolveDNS "127.0.0.1" }}{{ fail "rendered past resolveDNS" }}`)
	cases := map[string]struct {
		options []func(*ReleaseReconciler)
		want    string
	}{
		"default":    {nil, "error calling resolveDNS: resolveDNS is disabled by platform policy"},
		"disallowed": {[]func(*ReleaseReconciler){allowDNSLookups(false)}, "error calling resolveDNS: resolveDNS is disabled by platform policy"},
		"allowed":    {[]func(*ReleaseReconciler){allowDNSLookups(true)}, "error calling fail: rendered past resolveDNS"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := reconcileRelease(t, manifest, "", "", c.options...)
			if !strings.Contains(got.status, c.want) {
				t.Fatalf("got status %q, want one containing %q", got.status, c.want)
			}
		})
	}
}

const connectionRefDefaultPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
schema:
  parameters:
    properties:
      db:
        type: connectionRef
        contract: database
        default: '{{ resolveDNS "localhost" }}'
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
    values: |
      db: {{ .Parameters.db.host }}
`

func TestReleaseParametersNeverGetResolveDNS(t *testing.T) {
	cases := map[string]struct {
		manifest   string
		parameters string
	}{
		"Release parameters":                   {fmt.Sprintf(reconciledPackage, "replicas: 1"), "{\"replicas\":\"{{ resolveDNS `localhost` }}\"}"},
		"templated default of a connectionRef": {connectionRefDefaultPackage, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := reconcileRelease(t, c.manifest, c.parameters, "", allowDNSLookups(true))
			if want := `function "resolveDNS" not defined`; !strings.Contains(got.status, want) {
				t.Fatalf("got status %q, want one containing %q", got.status, want)
			}
		})
	}
}
