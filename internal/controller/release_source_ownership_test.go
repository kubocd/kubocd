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
	"context"
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/global"
	"net/http"
	"reflect"
	"strings"
	"testing"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const foreignSourceData = "FOREIGN_SOURCE_DATA"

const (
	sourceSpecEqual      = "equal"
	sourceOwnerRelease   = "demo"
	sourceNotReady       = "not-ready"
	sourceOwnerNamespace = "apps"
	sourceOwnerName      = "kcd-demo"
	sourceSpecDifferent  = "different"
	sourceBeingDeleted   = "deleting"
	sourcePolicyInsecure = "insecure"
	sourcePolicyRollback = "rollback"
)

// refusedArtifactTransport counts artifact requests without a network listener.
type refusedArtifactTransport struct{ requests int }

func (r *refusedArtifactTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	r.requests++
	return nil, errors.New("artifact request refused by test transport")
}

func TestOCIRepositoryRequiresReleaseController(t *testing.T) {
	testSourceController(t, sourcev1.OCIRepositoryKind, []string{sourceSpecEqual, sourceSpecDifferent, sourceNotReady, sourcePolicyInsecure, sourcePolicyRollback})
}

func TestHelmRepositoryRequiresReleaseController(t *testing.T) {
	testSourceController(t, sourcev1.HelmRepositoryKind, []string{sourceSpecEqual, sourceSpecDifferent, sourceNotReady, "previous-revision", "no-revision", sourceBeingDeleted})
}

func testSourceController(t *testing.T, kind string, states []string) {
	t.Helper()
	for _, owner := range []string{"current", "no-refs", "other-release", "old-uid", "controller-false", "controller-nil"} {
		for _, state := range states {
			t.Run(owner+"/"+state, func(t *testing.T) {
				b, release, source := prepareSourceOwner(t, kind, state, owner)
				before := source.DeepCopyObject().(client.Object)
				writes := 0
				sameKind := func(obj client.Object) bool { return reflect.TypeOf(obj) == reflect.TypeOf(source) }
				b.reconciler.Client = interceptor.NewClient(b.client, interceptor.Funcs{
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						if sameKind(obj) {
							writes++
						}
						return c.Patch(ctx, obj, patch, opts...)
					},
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						if sameKind(obj) {
							writes++
						}
						return c.Delete(ctx, obj, opts...)
					},
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if sameKind(obj) {
							writes++
						}
						return c.Create(ctx, obj, opts...)
					},
				})
				op := &releaseOperation{ctx: context.Background(), logger: b.reconciler.Logger, release: release,
					ociRepositoryName: sourceOwnerName, helmRepositoryName: sourceOwnerName}
				var returned bool
				var wait string
				var rerr ReconcileError
				if kind == sourcev1.OCIRepositoryKind {
					got, message, err := b.reconciler.handleOciRepository(op, global.PackageContentMediaType, "extract")
					returned, wait, rerr = got != nil, message, err
				} else {
					got, message, err := b.reconciler.handleHelmRepository(op, reconcileRevision, "http://"+b.reconciler.HelmRepoAdvAddr+"/hr/apps/demo")
					returned, wait, rerr = got != nil, message, err
				}
				if owner == "current" {
					assertOwnedSource(t, b, kind, state, returned, wait, rerr, writes)
					return
				}
				want := kind + " 'kcd-demo' exists and is not controlled by this Release"
				reason := kind + "Owner"
				assertSourceOwnerError(t, rerr, want, reason)
				if returned || wait != "" || writes != 0 {
					t.Errorf("foreign source: returned=%v wait=%q writes=%d", returned, wait, writes)
				}
				if err := b.client.Get(context.Background(), client.ObjectKeyFromObject(source), source); err != nil {
					t.Fatalf("foreign source lost: %v", err)
				}
				if !reflect.DeepEqual(source, before) {
					t.Error("foreign source changed")
				}
				// A cold folder makes an accepted OCI artifact reach the counting transport.
				if kind == sourcev1.OCIRepositoryKind {
					coldFolder(t)(b.reconciler)
				}
				transport := &refusedArtifactTransport{}
				b.reconciler.Fetcher.client.HTTPClient.Transport = transport
				got := b.reconcile(t)
				assertSourceOwnerError(t, got.err, want, reason)
				if phaseOf(t, b) != kv1alpha1.ReleasePhaseError || got.status != want {
					t.Errorf("phase/status = %q/%q", phaseOf(t, b), got.status)
				}
				if !hasEvent(got.events, reason) {
					t.Errorf("missing owner event: %q", got.events)
				}
				for sink, text := range got.sinks() {
					if strings.Contains(text, foreignSourceData) {
						t.Errorf("%s quotes foreign source content: %s", sink, text)
					}
				}
				if transport.requests != 0 || writes != 0 {
					t.Errorf("foreign source writes=%d artifact requests=%d", writes, transport.requests)
				}
			})
		}
	}
}

func assertSourceOwnerError(t *testing.T, err error, want, reason string) {
	t.Helper()
	rerr, ok := err.(ReconcileError)
	if !ok {
		t.Errorf("got %T %v, want retryable %s", err, err, reason)
		return
	}
	if rerr.IsFatal() || rerr.GetEventReason() != reason || rerr.Error() != want || rerr.GetBaseError().Error() != want {
		t.Errorf("owner error: fatal=%v reason=%q message=%q, want %q", rerr.IsFatal(), rerr.GetEventReason(), rerr.Error(), want)
	}
}

func prepareSourceOwner(t *testing.T, kind, state, owner string) (*releaseTestbed, *kv1alpha1.Release, client.Object) {
	t.Helper()
	b := newReleaseTestbed(t, fmt.Sprintf(reconciledPackage, "answer: 42"), "", "")
	release := &kv1alpha1.Release{}
	key := client.ObjectKey{Namespace: sourceOwnerNamespace, Name: sourceOwnerRelease}
	if err := b.client.Get(context.Background(), key, release); err != nil {
		t.Fatal(err)
	}
	if state == sourcePolicyInsecure || state == sourcePolicyRollback {
		release.Spec.Package.Insecure = true
		if err := b.client.Update(context.Background(), release); err != nil {
			t.Fatal(err)
		}
		b.reconciler.OCIRepositoryOptions.AllowReleaseInsecureSource = state == sourcePolicyRollback
	}
	GroomRelease(release, b.reconciler.Logger, b.reconciler.ConfigStore)
	var source client.Object = &sourcev1.HelmRepository{}
	if kind == sourcev1.OCIRepositoryKind {
		source = &sourcev1.OCIRepository{}
	}
	sourceKey := client.ObjectKey{Namespace: sourceOwnerNamespace, Name: sourceOwnerName}
	if err := b.client.Get(context.Background(), sourceKey, source); err != nil {
		t.Fatal(err)
	}
	switch obj := source.(type) {
	case *sourcev1.OCIRepository:
		if err := PopulateOciRepository(obj, release, global.PackageContentMediaType, "extract", b.reconciler.ConfigStore,
			OCIRepositoryOptions{AllowReleaseInsecureSource: true}); err != nil {
			t.Fatal(err)
		}
		if state == sourceSpecDifferent {
			obj.Spec.URL = "oci://foreign.example/" + foreignSourceData
		}
		if state == sourceNotReady {
			obj.Status.Conditions[0].Status = metav1.ConditionFalse
			obj.Status.Conditions[0].Message = foreignSourceData
		}
		obj.Status.Artifact.URL = "https://foreign.example/" + foreignSourceData
		obj.Status.Artifact.Digest = "sha256:" + strings.Repeat("0", 64)
	case *sourcev1.HelmRepository:
		PopulateHelmRepository(obj, release, "http://"+b.reconciler.HelmRepoAdvAddr+"/hr/apps/demo")
		if state == sourceSpecDifferent {
			obj.Spec.URL = "https://foreign.example/" + foreignSourceData
		}
		if state == sourceNotReady {
			obj.Status.Conditions[0].Status = metav1.ConditionFalse
			obj.Status.Conditions[0].Message = foreignSourceData
		}
		if state == "previous-revision" {
			obj.Annotations[packageRevisionAnnotation] = olderRevision
		}
		if state == "no-revision" {
			obj.Annotations = nil
		}
	}
	refs := source.GetOwnerReferences()
	switch owner {
	case "no-refs":
		refs = nil
	case "other-release":
		refs[0].Name, refs[0].UID = "unrelated-release", "other-release-uid"
	case "old-uid":
		refs[0].UID = "old-release-uid"
	case "controller-false":
		controlled := false
		refs[0].Controller = &controlled
	case "controller-nil":
		refs[0].Controller = nil
	}
	source.SetOwnerReferences(refs)
	if state == sourceBeingDeleted {
		source.SetFinalizers([]string{"test.kubotal.io/hold"})
	}
	if err := b.client.Update(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if state == sourceBeingDeleted {
		if err := b.client.Delete(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.client.Get(context.Background(), sourceKey, source); err != nil {
		t.Fatal(err)
	}
	return b, release, source
}

func assertOwnedSource(t *testing.T, b *releaseTestbed, kind, state string, returned bool, wait string, rerr ReconcileError, writes int) {
	t.Helper()
	if state == sourcePolicyInsecure {
		if rerr == nil || !rerr.IsFatal() || rerr.GetEventReason() != "ReleaseInsecureSource" {
			t.Fatalf("owned policy: %v", rerr)
		}
	} else if rerr != nil {
		t.Fatalf("owned source refused: %v", rerr)
	}
	if state == sourceSpecEqual && !returned {
		t.Fatal("owned ready source was not returned")
	}
	if kind == sourcev1.OCIRepositoryKind && state == sourceSpecEqual {
		coldFolder(t)(b.reconciler)
		transport := &refusedArtifactTransport{}
		b.reconciler.Fetcher.client.HTTPClient.Transport = transport
		b.reconcile(t)
		if transport.requests != 1 {
			t.Fatalf("owned artifact control: got %d requests, want 1", transport.requests)
		}
	}
	if state == sourceBeingDeleted && (writes != 0 || wait != waitHelmRepository) {
		t.Fatalf("owned deletion: writes=%d wait=%q", writes, wait)
	}
}
