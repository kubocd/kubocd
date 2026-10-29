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
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"reflect"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"
)

const contextDecodeSentinel = "CONTEXT_DECODE_SENTINEL"
const contextDecodeMessage = "context decoding error, message withheld, as it can quote the data"
const contextDecodeDuplicate = `{"CONTEXT_DECODE_SENTINEL":1,"CONTEXT_DECODE_SENTINEL":2}`
const contextDecodeMalformed = "value: !!int CONTEXT_DECODE_SENTINEL"

func assertContextDiagnostic(t *testing.T, err error, want, reason string, fatal bool) {
	t.Helper()
	rerr, ok := err.(ReconcileError)
	if !ok {
		t.Fatalf("got %T (%v), want ReconcileError", err, err)
	}
	if rerr.Error() != want || rerr.GetEventReason() != reason || rerr.IsFatal() != fatal {
		t.Errorf("got %q, reason %q, fatal %v, want %q, %q, %v", rerr.Error(), rerr.GetEventReason(), rerr.IsFatal(), want, reason, fatal)
	}
	if rerr.GetBaseError() == nil || rerr.GetBaseError().Error() != want {
		t.Errorf("base error = %v, want %q", rerr.GetBaseError(), want)
	}
	assertContextErrorChain(t, err)
}

func assertContextErrorChain(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), contextDecodeSentinel) {
		t.Errorf("error chain quotes Context data: %v", err)
	}
	if rerr, ok := err.(ReconcileError); ok {
		assertContextErrorChain(t, rerr.GetBaseError())
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range multi.Unwrap() {
			assertContextErrorChain(t, cause)
		}
	} else {
		assertContextErrorChain(t, errors.Unwrap(err))
	}
}

func assertContextSinks(t *testing.T, sinks map[string]string) {
	t.Helper()
	for name, text := range sinks {
		if strings.Contains(text, contextDecodeSentinel) {
			t.Errorf("%s quotes Context data: %s", name, text)
		}
	}
}

func assertContextEvent(t *testing.T, events []string, reason, message string) {
	t.Helper()
	want := "Warning " + reason + " " + message
	if len(events) != 1 || events[0] != want {
		t.Errorf("events = %q, want [%q]", events, want)
	}
}

// The fake client retains duplicate keys in raw JSON. These bytes are the
// input to the strict decoder and its data-quoting diagnostic.
func TestContextDecodeDiagnosticsAreWithheld(t *testing.T) {
	var decoded map[string]interface{}
	if err := yaml.UnmarshalStrict([]byte(contextDecodeDuplicate), &decoded); err == nil || !strings.Contains(err.Error(), contextDecodeSentinel) {
		t.Fatalf("strict decoder control did not quote the sentinel: %v", err)
	}
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprintf("status-refused=%v", refuse), func(t *testing.T) {
			object := contextWithParents("invalid")
			object.Spec.Context = &apiextensionsv1.JSON{Raw: []byte(contextDecodeDuplicate)}
			base := contextClient(t, object)
			writes := 0
			c := contextDecodeClient(base, "invalid", contextDecodeDuplicate, false, refuse, &writes)
			want := "unable to unmarshal kubo context: " + contextDecodeMessage
			for attempt := 1; attempt <= 2; attempt++ {
				got := reconcileContext(t, c, "invalid")
				assertContextDiagnostic(t, got.err, want, "InvalidContext", false)
				assertContextSinks(t, map[string]string{"events": strings.Join(got.events, "\n"), "log": got.log})
				if refuse {
					if got.context.Status.Phase != "" || len(got.events) != 0 || writes != attempt {
						t.Errorf("failed status write: phase %q, events %q, writes %d", got.context.Status.Phase, got.events, writes)
					}
				} else {
					if got.context.Status.Phase != kv1alpha1.ContextPhaseError || writes != 1 {
						t.Errorf("phase %q, writes %d, want ERROR written once", got.context.Status.Phase, writes)
					}
					assertContextEvent(t, got.events, "InvalidContext", want)
				}
			}
		})
	}
}

// contextDecodeClient supplies raw decoder input after a successful Get.
// Status values take precedence over the valid spec. Duplicate keys persist
// across reconciliations even when the fake status writer normalizes JSON.
// Refused status writes leave the stored object unchanged.
func contextDecodeClient(base client.WithWatch, name, raw string, status, refuse bool, writes *int) client.WithWatch {
	return interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if kctx, ok := obj.(*kv1alpha1.Context); ok && kctx.Name == name {
				malformed := &apiextensionsv1.JSON{Raw: []byte(raw)}
				if status {
					kctx.Status.Context = malformed
				} else {
					kctx.Spec.Context = malformed
					kctx.Status.Context = nil
				}
			}
			return nil
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if sub == "status" {
				*writes++
				if refuse {
					return errors.New("status write refused")
				}
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
}

func contextMergeDecoderControl(t *testing.T) {
	t.Helper()
	_, err := Merge(nil, &apiextensionsv1.JSON{Raw: []byte(contextDecodeMalformed)})
	if err == nil || !strings.Contains(err.Error(), contextDecodeSentinel) {
		t.Fatalf("non-strict merge control did not quote the sentinel: %v", err)
	}
}

func TestContextParentMergeDiagnosticsAreWithheld(t *testing.T) {
	contextMergeDecoderControl(t)
	for _, status := range []bool{false, true} {
		for _, refuse := range []bool{false, true} {
			t.Run(fmt.Sprintf("status-values=%v/status-refused=%v", status, refuse), func(t *testing.T) {
				parent := ready(contextWithParents("parent"))
				child := contextWithParents("child", "parent")
				writes := 0
				c := contextDecodeClient(contextClient(t, parent, child), "parent", contextDecodeMalformed, status, refuse, &writes)
				got := reconcileContext(t, c, "child")
				want := "unable to merge context from 'apps:parent': " + contextDecodeMessage
				assertContextSinks(t, map[string]string{"events": strings.Join(got.events, "\n"), "log": got.log})
				if writes != 1 {
					t.Errorf("status writes = %d, want 1", writes)
				}
				if refuse {
					assertContextDiagnostic(t, got.err, want, "ContextError", true)
					if len(got.events) != 0 || got.context.Status.Phase != "" {
						t.Errorf("failed status write: phase %q, events %q", got.context.Status.Phase, got.events)
					}
				} else {
					if got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseError {
						t.Errorf("error %v, phase %q, want nil and ERROR", got.err, got.context.Status.Phase)
					}
					assertContextEvent(t, got.events, "ContextError", want)
					if !strings.Contains(got.log, want) || !strings.Contains(got.log, "Wait for this to be fixed") {
						t.Errorf("missing fatal diagnostic in log: %s", got.log)
					}
				}
			})
		}
	}
}

func TestReleaseContextDecodeDiagnosticsAreWithheld(t *testing.T) {
	contextMergeDecoderControl(t)
	for _, status := range []bool{false, true} {
		for _, refuse := range []bool{false, true} {
			t.Run(fmt.Sprintf("status-values=%v/status-refused=%v", status, refuse), func(t *testing.T) {
				b := newReleaseTestbed(t, fmt.Sprintf(reconciledPackage, "answer: 42"), "", `{}`)
				writes := 0
				b.reconciler.Client = contextDecodeClient(b.client, "cluster", contextDecodeMalformed, status, refuse, &writes)
				release := &kv1alpha1.Release{}
				key := client.ObjectKey{Namespace: "apps", Name: "demo"}
				if err := b.client.Get(context.Background(), key, release); err != nil {
					t.Fatal(err)
				}
				values, refs, err := ComputeContext(context.Background(), b.reconciler.Client, release, b.reconciler.ConfigStore, nil)
				want := "unable to merge context: " + contextDecodeMessage
				assertContextDiagnostic(t, err, want, "ContextMerge", true)
				if values != nil || refs != nil {
					t.Errorf("failed merge returned values %v, refs %v", values, refs)
				}
				for attempt := 1; attempt <= 2; attempt++ {
					got := b.reconcile(t)
					assertContextSinks(t, got.sinks())
					stored := &kv1alpha1.Release{}
					if err := b.client.Get(context.Background(), key, stored); err != nil {
						t.Fatal(err)
					}
					if refuse {
						if writes != attempt || stored.Status.Phase != "" || got.status != "" {
							t.Errorf("failed status write: writes %d, phase %q, message %q", writes, stored.Status.Phase, got.status)
						}
						if attempt == 1 {
							if got.err != nil || got.result.RequeueAfter != 200*time.Millisecond {
								t.Errorf("first status failure: result %v, error %v", got.result, got.err)
							}
							assertContextEvent(t, got.events, "ContextMerge", want)
						} else {
							assertContextDiagnostic(t, got.err, want, "ContextMerge", true)
							if len(got.events) != 0 || got.result.RequeueAfter != 0 {
								t.Errorf("second status failure: events %q, result %v", got.events, got.result)
							}
						}
					} else {
						if got.err != nil || got.result.RequeueAfter != 0 || stored.Status.Phase != kv1alpha1.ReleasePhaseError || got.status != want {
							t.Errorf("result %v, error %v, phase %q, status %q", got.result, got.err, stored.Status.Phase, got.status)
						}
						assertContextEvent(t, got.events, "ContextMerge", want)
					}
					if !strings.Contains(got.log, want) || !strings.Contains(got.log, "Wait for this to be fixed") {
						t.Errorf("missing diagnostic in log: %s", got.log)
					}
				}
			})
		}
	}
}

func TestContextDecodeCompatibility(t *testing.T) {
	t.Run("nil-and-empty", func(t *testing.T) {
		for _, raw := range []string{"", `{}`} {
			object := contextWithParents("empty")
			object.Spec.Context = nil
			if raw != "" {
				object.Spec.Context = &apiextensionsv1.JSON{Raw: []byte(raw)}
			}
			got := reconcileContext(t, contextClient(t, object), "empty")
			if got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseReady || len(got.events) != 0 {
				t.Fatalf("empty values: error %v, phase %q, events %q", got.err, got.context.Status.Phase, got.events)
			}
		}
	})
	t.Run("parent-status-and-own-values", func(t *testing.T) {
		parent := ready(contextWithParents("parent"))
		parent.Spec.Context = &apiextensionsv1.JSON{Raw: []byte(`{"db":{"host":"spec-host"}}`)}
		parent.Status.Context = &apiextensionsv1.JSON{Raw: []byte(`{"db":{"host":"status","port":5432}}`)}
		child := contextWithParents("child", "parent")
		child.Spec.Context = &apiextensionsv1.JSON{Raw: []byte(`{"db":{"port":5433}}`)}
		got := reconcileContext(t, contextClient(t, parent, child), "child")
		want := &apiextensionsv1.JSON{Raw: []byte(`{"db":{"host":"status","port":5433}}`)}
		if got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseReady || !sameJSON(got.context.Status.Context, want) {
			t.Fatalf("parent merge: error %v, status %+v", got.err, got.context.Status)
		}
	})
	for _, tc := range []struct {
		name, spec, status string
		want               map[string]interface{}
	}{
		{"spec-values", `{"db":{"host":"spec-host"}}`, "", map[string]interface{}{"db": map[string]interface{}{"host": "spec-host"}}},
		{"status", `{"db":{"host":"spec-host"}}`, `{"db":{"host":"status"}}`, map[string]interface{}{"db": map[string]interface{}{"host": "status"}}},
		{"duplicates", `{"value":1,"value":2}`, "", map[string]interface{}{"value": float64(2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := ready(contextWithParents("cluster"))
			object.Spec.Context = &apiextensionsv1.JSON{Raw: []byte(tc.spec)}
			object.Status.Context = nil
			if tc.status != "" {
				object.Status.Context = &apiextensionsv1.JSON{Raw: []byte(tc.status)}
			}
			release := newTestRelease("demo", "")
			release.Spec.SkipDefaultContext = true
			release.Spec.Contexts = []kv1alpha1.NamespacedName{{Namespace: "apps", Name: "cluster"}}
			values, refs, err := ComputeContext(context.Background(), contextClient(t, object), release, newTestStore(t), nil)
			if err != nil || !reflect.DeepEqual(values, tc.want) || !reflect.DeepEqual(refs, release.Spec.Contexts) {
				t.Fatalf("values %v, refs %v, error %v, want %v", values, refs, err, tc.want)
			}
		})
	}
}
