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
	"encoding/json"
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/global"
	"kubocd/internal/rolestore"
	"reflect"
	"strings"
	"testing"
	"time"

	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	verifyNonmatchingRedirect = "nonmatching redirect"
	verifyNoRedirect          = "no redirect"
	verifySourceRepository    = "origin.example/packages/demo"
	verifyMirrorRepository    = "mirror.example/cache/demo"
	verifySourceTag           = "1.2.3"
	verifyMirrorTag           = "4.5.6"
	verifyOperation           = "extract"
	verifyRollbackFailure     = "rollback did not restore redirect verification behavior"
)

// verificationOf gives each source distinct verification keys and identities.
func verificationOf(name string) *sourcev1.OCIRepositoryVerification {
	return &sourcev1.OCIRepositoryVerification{
		Provider:          "cosign",
		SecretRef:         &fluxmeta.LocalObjectReference{Name: name + "-keys"},
		MatchOIDCIdentity: []sourcev1.OIDCIdentityMatch{{Issuer: "https://" + name + ".example", Subject: name + "-signer"}},
	}
}

// verificationAddons gives all addons nonzero values distinguishable by source.
func verificationAddons(name string) kv1alpha1.OciAddOn {
	ignore := name + "-ignored"
	return kv1alpha1.OciAddOn{
		Provider: "aws", SecretRef: &fluxmeta.LocalObjectReference{Name: name + "-pull"},
		Verify: verificationOf(name), ServiceAccountName: name + "-account",
		CertSecretRef:  &fluxmeta.LocalObjectReference{Name: name + "-cert"},
		ProxySecretRef: &fluxmeta.LocalObjectReference{Name: name + "-proxy"},
		Ignore:         &ignore, Insecure: true, Suspend: true,
	}
}

// verificationRelease is a Release whose package has distinctive addon values.
func verificationRelease() *kv1alpha1.Release {
	timeout := metav1.Duration{Duration: 3 * time.Minute}
	return &kv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "verify-demo", Namespace: "verify-apps", UID: types.UID("verify-release-uid")},
		Spec: kv1alpha1.ReleaseSpec{Package: kv1alpha1.PackageSourceSpec{
			Repository: verifySourceRepository, Tag: verifySourceTag, OciAddOn: verificationAddons("release"),
			Interval: metav1.Duration{Duration: 7 * time.Minute}, Timeout: &timeout,
		}},
	}
}

// verificationRedirect changes both the repository and the tag of the fixture package.
func verificationRedirect() *kv1alpha1.PackageRedirectSpec {
	return &kv1alpha1.PackageRedirectSpec{
		OldPrefix: verifySourceRepository + ":" + verifySourceTag,
		NewPrefix: verifyMirrorRepository + ":" + verifyMirrorTag,
	}
}

// expectedVerificationSpec describes the complete source spec required by a fixture.
func expectedVerificationSpec(repository, tag string, addon kv1alpha1.OciAddOn, interval metav1.Duration, timeout *metav1.Duration) sourcev1.OCIRepositorySpec {
	return sourcev1.OCIRepositorySpec{
		URL: "oci://" + repository, Reference: &sourcev1.OCIRepositoryRef{Tag: tag},
		Provider: addon.Provider, SecretRef: addon.SecretRef, Verify: addon.Verify,
		ServiceAccountName: addon.ServiceAccountName, CertSecretRef: addon.CertSecretRef, ProxySecretRef: addon.ProxySecretRef,
		Interval: interval, Timeout: timeout, Ignore: addon.Ignore, Insecure: addon.Insecure, Suspend: addon.Suspend,
		LayerSelector: &sourcev1.OCILayerSelector{MediaType: global.PackageContentMediaType, Operation: verifyOperation},
	}
}

// TestPopulateOciRepositoryVerifyPrecedence checks inheritance without changing the other redirect addons.
func TestPopulateOciRepositoryVerifyPrecedence(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		for _, releaseVerify := range []bool{false, true} {
			for _, redirectVerify := range []bool{false, true} {
				for _, variant := range []string{"zero addons", "configured addons", "addons only", verifyNoRedirect, verifyNonmatchingRedirect} {
					t.Run(fmt.Sprintf("%s/release=%t/redirect=%t/rollback=%t", variant, releaseVerify, redirectVerify, rollback), func(t *testing.T) {
						release := verificationRelease()
						if !releaseVerify {
							release.Spec.Package.Verify = nil
						}
						redirect := verificationRedirect()
						if variant == "configured addons" {
							redirect.OciAddOn = verificationAddons("redirect")
							redirect.Provider = "gcp"
							redirect.Interval = metav1.Duration{Duration: 11 * time.Minute}
							redirect.Timeout = &metav1.Duration{Duration: 2 * time.Minute}
						}
						redirect.Verify = nil
						if redirectVerify {
							redirect.Verify = verificationOf("redirect")
						}
						repository, tag := verifyMirrorRepository, verifyMirrorTag
						if variant == "addons only" {
							redirect.NewPrefix = ""
							repository, tag = verifySourceRepository, verifySourceTag
						}
						spec := kv1alpha1.ConfigSpec{PackageRedirects: []*kv1alpha1.PackageRedirectSpec{redirect}}
						matched := variant != verifyNoRedirect && variant != verifyNonmatchingRedirect
						if !matched {
							release.Spec.Package.Insecure = false
						}
						if variant == verifyNoRedirect {
							spec.PackageRedirects = nil
						}
						if variant == verifyNonmatchingRedirect {
							redirect.OldPrefix = "unmatched.example/"
						}
						pkg := release.Spec.Package
						original := expectedVerificationSpec(pkg.Repository, pkg.Tag, pkg.OciAddOn, pkg.Interval, pkg.Timeout)
						want := original
						if matched {
							want = expectedVerificationSpec(repository, tag, redirect.OciAddOn, redirect.Interval, redirect.Timeout)
							if want.Verify == nil && !rollback {
								want.Verify = pkg.Verify
							}
						}
						destination := &sourcev1.OCIRepository{Spec: original}
						err := PopulateOciRepository(destination, release, global.PackageContentMediaType, verifyOperation, storeOf(t, spec),
							OCIRepositoryOptions{AllowRedirectVerifyDrop: rollback})
						if err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(destination.Spec.Verify, want.Verify) {
							message := "matching redirect erased Release verification"
							if rollback {
								message = verifyRollbackFailure
							}
							t.Fatalf("%s: got %#v, want %#v", message, destination.Spec.Verify, want.Verify)
						}
						if !reflect.DeepEqual(destination.Spec, want) {
							t.Fatalf("redirect changed unrelated spec fields: got %#v, want %#v", destination.Spec, want)
						}
					})
				}
			}
		}
	}
}

// verificationTestbed supplies real configuration and an API client that persists source writes.
func verificationTestbed(t *testing.T, release *kv1alpha1.Release, store configstore.ConfigStore, options OCIRepositoryOptions, objects ...client.Object) (*ReleaseReconciler, *releaseOperation) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sourcev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects = append(objects, release)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&kv1alpha1.Release{}).Build()
	logger := logr.Discard()
	reconciler := &ReleaseReconciler{
		Client: c, EventRecorder: record.NewFakeRecorder(10), Logger: logger,
		ConfigStore: store, RoleStore: rolestore.New(store, logger), OCIRepositoryOptions: options,
	}
	op := &releaseOperation{
		ctx: context.Background(), logger: logger, release: release,
		ociRepositoryName: fmt.Sprintf(OciRepositoryNameFormat, release.Name),
	}
	return reconciler, op
}

// fetchedVerificationSource reads the persisted source instead of relying on an in-memory patch target.
func fetchedVerificationSource(t *testing.T, r *ReleaseReconciler, op *releaseOperation) *sourcev1.OCIRepository {
	t.Helper()
	source := &sourcev1.OCIRepository{}
	if err := r.Get(op.ctx, types.NamespacedName{Name: op.ociRepositoryName, Namespace: op.release.Namespace}, source); err != nil {
		t.Fatal(err)
	}
	return source
}

// TestCreateOciRepositoryPreservesReleaseVerify checks default inheritance and rollback on creation.
func TestCreateOciRepositoryPreservesReleaseVerify(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", rollback), func(t *testing.T) {
			release := verificationRelease()
			store := storeOf(t, kv1alpha1.ConfigSpec{PackageRedirects: []*kv1alpha1.PackageRedirectSpec{verificationRedirect()}})
			r, op := verificationTestbed(t, release, store, OCIRepositoryOptions{AllowRedirectVerifyDrop: rollback})
			if err := r.createOciRepository(op, global.PackageContentMediaType, verifyOperation); err != nil {
				t.Fatal(err)
			}
			got := fetchedVerificationSource(t, r, op)
			want := release.Spec.Package.Verify
			message := "created OCIRepository lost Release verification"
			if rollback {
				want, message = nil, verifyRollbackFailure
			}
			if !reflect.DeepEqual(got.Spec.Verify, want) {
				t.Fatalf("%s: got %#v, want %#v", message, got.Spec.Verify, want)
			}
			if !metav1.IsControlledBy(got, release) {
				t.Fatal("created OCIRepository has no Release controller owner reference")
			}
		})
	}
}

// TestPatchOciRepositoryPreservesReleaseVerify checks inheritance, replacement and clearing in persisted updates.
func TestPatchOciRepositoryPreservesReleaseVerify(t *testing.T) {
	cases := []struct {
		name                         string
		old, release, redirect, want *sourcev1.OCIRepositoryVerification
		rollback                     bool
	}{
		{name: "inherit", release: verificationOf("release"), want: verificationOf("release")},
		{name: "replace stale", old: verificationOf("stale"), release: verificationOf("release"), redirect: verificationOf("redirect"), want: verificationOf("redirect")},
		{name: "clear stale", old: verificationOf("stale")},
		{name: "rollback", old: verificationOf("release"), release: verificationOf("release"), rollback: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := verificationRelease()
			release.Spec.Package.Verify = tc.release
			redirect := verificationRedirect()
			redirect.Verify = tc.redirect
			store := storeOf(t, kv1alpha1.ConfigSpec{PackageRedirects: []*kv1alpha1.PackageRedirectSpec{redirect}})
			r, op := verificationTestbed(t, release, store, OCIRepositoryOptions{AllowRedirectVerifyDrop: tc.rollback})
			existing := &sourcev1.OCIRepository{
				ObjectMeta: metav1.ObjectMeta{Name: op.ociRepositoryName, Namespace: release.Namespace, UID: types.UID("verify-source-uid")},
				Spec:       sourcev1.OCIRepositorySpec{URL: "oci://" + verifyMirrorRepository, Verify: tc.old},
			}
			if err := ctrl.SetControllerReference(release, existing, r.Scheme()); err != nil {
				t.Fatal(err)
			}
			if err := r.Create(op.ctx, existing); err != nil {
				t.Fatal(err)
			}
			target := fetchedVerificationSource(t, r, op)
			if _, err := r.patchOciRepository(op, target, global.PackageContentMediaType, verifyOperation); err != nil {
				t.Fatal(err)
			}
			got := fetchedVerificationSource(t, r, op)
			message := "patched OCIRepository lost Release verification"
			if tc.rollback {
				message = verifyRollbackFailure
			}
			if !reflect.DeepEqual(got.Spec.Verify, tc.want) {
				t.Fatalf("%s: got %#v, want %#v", message, got.Spec.Verify, tc.want)
			}
		})
	}
}

const (
	sourceMetadataKey         = "kept"
	insecurePolicyMessage     = "Release.spec.package.insecure is not allowed without a matching packageRedirect"
	insecurePolicyReason      = "ReleaseInsecureSource"
	insecureRepairMessage     = "could not disable insecure transport on associated OCIRepository"
	sourceCreateFailureReason = "OCIRepositoryCreateFailed"
	sourcePatchFailureReason  = "OCIRepositoryPatchFailed"
	sourceCreatePath          = "create"
	sourceUpdatePath          = "update"
	sourceSecurePath          = "already secure"
	sourceMatchingRedirect    = "matching redirect"
	sourceAddonsRedirect      = "addons-only redirect"
	sourceAPICanary           = "api-response-canary"
)

// TestPopulateOciRepositoryInsecurePolicy checks that only unredirected Release transport is refused.
func TestPopulateOciRepositoryInsecurePolicy(t *testing.T) {
	for _, variant := range []string{verifyNoRedirect, verifyNonmatchingRedirect, sourceMatchingRedirect, sourceAddonsRedirect} {
		for _, releaseInsecure := range []bool{false, true} {
			for _, redirectInsecure := range []bool{false, true} {
				for _, rollback := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/release=%t/redirect=%t/rollback=%t", variant, releaseInsecure, redirectInsecure, rollback), func(t *testing.T) {
						release := verificationRelease()
						release.Spec.Package.Insecure = releaseInsecure
						redirect := verificationRedirect()
						redirect.Insecure = redirectInsecure
						spec := kv1alpha1.ConfigSpec{PackageRedirects: []*kv1alpha1.PackageRedirectSpec{redirect}}
						matched := variant == sourceMatchingRedirect || variant == sourceAddonsRedirect
						switch variant {
						case verifyNoRedirect:
							spec.PackageRedirects = nil
							release.Spec.Package.Verify = nil
						case verifyNonmatchingRedirect:
							redirect.OldPrefix = "unmatched.example/"
						case sourceAddonsRedirect:
							redirect.NewPrefix = ""
						}
						pkg := release.Spec.Package
						destination := &sourcev1.OCIRepository{Spec: expectedVerificationSpec(verifyMirrorRepository, verifyMirrorTag, verificationAddons("untouched"), pkg.Interval, pkg.Timeout)}
						before := destination.DeepCopy()
						err := PopulateOciRepository(destination, release, global.PackageContentMediaType, verifyOperation, storeOf(t, spec),
							OCIRepositoryOptions{AllowReleaseInsecureSource: rollback})
						if !matched && releaseInsecure && !rollback {
							if err == nil {
								t.Fatal("unredirected insecure Release was accepted")
							}
							if err.Error() != insecurePolicyMessage {
								t.Fatalf("got %v, want %s", err, insecurePolicyMessage)
							}
							if !reflect.DeepEqual(destination, before) {
								t.Fatal("refused population changed the destination")
							}
							return
						}
						if err != nil {
							if matched {
								t.Fatalf("matching redirect must replace Release insecure: %v", err)
							}
							t.Fatalf("rollback refused direct insecure source: %v", err)
						}
						want := releaseInsecure
						if matched {
							want = redirectInsecure
						}
						if destination.Spec.Insecure != want {
							t.Fatalf("insecure = %t, want %t", destination.Spec.Insecure, want)
						}
					})
				}
			}
		}
	}
}

// insecureSourceTestbed records source writes, API failures and reconciliation logs.
type insecureSourceTestbed struct {
	r                       *ReleaseReconciler
	op                      *releaseOperation
	original                *sourcev1.OCIRepository
	creates                 int
	patches                 [][]byte
	createError, patchError error
	logs                    []string
}

// newInsecureSourceTestbed prepares a Release and, on update paths, an owned source with a ready artifact.
func newInsecureSourceTestbed(t *testing.T, path string, spec kv1alpha1.ConfigSpec, options OCIRepositoryOptions) *insecureSourceTestbed {
	t.Helper()
	release := verificationRelease()
	release.Name = "insecure-name-canary"
	release.Spec.Package.Verify = verificationOf("verification-canary")
	r, op := verificationTestbed(t, release, storeOf(t, spec), options)
	op.request = ctrl.Request{NamespacedName: client.ObjectKeyFromObject(release)}
	b := &insecureSourceTestbed{r: r, op: op}
	op.logger = funcr.NewJSON(func(line string) { b.logs = append(b.logs, line) }, funcr.Options{})
	r.Logger = op.logger
	if path != sourceCreatePath {
		timeout := metav1.Duration{Duration: 9 * time.Minute}
		source := &sourcev1.OCIRepository{
			ObjectMeta: metav1.ObjectMeta{Name: op.ociRepositoryName, Namespace: release.Namespace, UID: types.UID("insecure-source-uid"),
				Generation: 2, Annotations: map[string]string{sourceMetadataKey: "annotation"}, Labels: map[string]string{sourceMetadataKey: "label"}},
			Spec: expectedVerificationSpec(verifyMirrorRepository, "stored-tag", verificationAddons("stored-canary"), metav1.Duration{Duration: 13 * time.Minute}, &timeout),
			Status: sourcev1.OCIRepositoryStatus{
				ObservedGeneration: 2,
				Conditions:         []metav1.Condition{{Type: fluxmeta.ReadyCondition, Status: metav1.ConditionTrue, ObservedGeneration: 2, Reason: fluxmeta.SucceededReason, LastTransitionTime: metav1.Now()}},
				Artifact:           &fluxmeta.Artifact{URL: "https://artifact-canary.example/package.tgz", Revision: "old-revision"},
			},
		}
		if path == sourceSecurePath {
			source.Spec.Insecure = false
		}
		if err := ctrl.SetControllerReference(release, source, r.Scheme()); err != nil {
			t.Fatal(err)
		}
		if err := r.Create(op.ctx, source); err != nil {
			t.Fatal(err)
		}
		b.original = fetchedVerificationSource(t, r, op).DeepCopy()
	}
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*sourcev1.OCIRepository); ok {
				b.creates++
				if b.createError != nil {
					return b.createError
				}
			}
			return c.Create(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*sourcev1.OCIRepository); ok {
				data, err := patch.Data(obj)
				if err != nil {
					return err
				}
				b.patches = append(b.patches, data)
				if b.patchError != nil {
					return b.patchError
				}
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	return b
}

// policyRefusal checks the handle boundary without permitting artifact consumption.
func (b *insecureSourceTestbed) policyRefusal(t *testing.T) ReconcileError {
	t.Helper()
	source, wait, err := b.r.handleOciRepository(b.op)
	if err == nil {
		t.Fatal("expected fatal ReleaseInsecureSource refusal, got no error")
	}
	if !err.IsFatal() {
		t.Fatalf("ReleaseInsecureSource refusal is retryable: %v", err)
	}
	if err.Error() != insecurePolicyMessage || err.GetBaseError().Error() != insecurePolicyMessage || err.GetEventReason() != insecurePolicyReason {
		t.Fatalf("got policy error %v with reason %q, want fixed policy diagnostic", err, err.GetEventReason())
	}
	if source != nil || wait != "" {
		t.Fatal("refused source returned an object or wait instead of blocking artifact consumption")
	}
	return err
}

// expectEvent reads exactly one expected event without blocking on an empty recorder.
func (b *insecureSourceTestbed) expectEvent(t *testing.T, want string) {
	t.Helper()
	events := b.r.EventRecorder.(*record.FakeRecorder).Events
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("event = %q, want %q", got, want)
		}
	default:
		t.Fatalf("missing event %q", want)
	}
	select {
	case extra := <-events:
		t.Fatalf("unexpected extra event %q", extra)
	default:
	}
}

// reportPolicyDiagnostic checks persisted status, retry behavior and the fixed event and error diagnostic.
func (b *insecureSourceTestbed) reportPolicyDiagnostic(t *testing.T, policy ReconcileError) {
	t.Helper()
	result, err := b.r.reportError(b.op, policy, false)
	if err != nil || result != (ctrl.Result{}) {
		t.Fatalf("fatal insecure refusal scheduled another reconciliation: %+v, %v", result, err)
	}
	release := &kv1alpha1.Release{}
	if err := b.r.Get(b.op.ctx, client.ObjectKeyFromObject(b.op.release), release); err != nil {
		t.Fatal(err)
	}
	if release.Status.Phase != kv1alpha1.ReleasePhaseError || release.Status.Message != insecurePolicyMessage {
		t.Fatalf("status = %+v, want ERROR and the fixed policy message", release.Status)
	}
	b.expectEvent(t, "Warning "+insecurePolicyReason+" "+insecurePolicyMessage)
	errorLogs := 0
	for _, line := range b.logs {
		fields := map[string]interface{}{}
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatal(err)
		}
		diagnostic, ok := fields["error"].(string)
		if !ok {
			continue
		}
		errorLogs++
		if diagnostic != insecurePolicyMessage {
			t.Fatalf("error diagnostic = %q, want the fixed policy message", diagnostic)
		}
		for _, canary := range []string{b.op.release.Name, b.op.release.Spec.Package.Repository, "verification-canary", "stored-canary"} {
			if strings.Contains(diagnostic, canary) {
				t.Fatalf("error diagnostic contains template data: %s", diagnostic)
			}
		}
	}
	if errorLogs == 0 {
		t.Fatal("no error-bearing policy log was captured")
	}
}

// checkCorrection checks the exact one-field patch and preservation of the stored source.
func (b *insecureSourceTestbed) checkCorrection(t *testing.T, attempts int, secured bool) {
	t.Helper()
	got := fetchedVerificationSource(t, b.r, b.op)
	if secured && got.Spec.Insecure {
		t.Fatal("existing OCIRepository still permits insecure transport")
	}
	want := b.original.DeepCopy()
	if secured {
		want.Spec.Insecure = false
	}
	got.ResourceVersion = want.ResourceVersion
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transport correction changed other source data: got %#v, want %#v", got, want)
	}
	if len(b.patches) != attempts || b.creates != 0 {
		t.Fatalf("got %d patches and %d creates, want %d corrective patches only", len(b.patches), b.creates, attempts)
	}
	for _, data := range b.patches {
		var patch map[string]interface{}
		if err := json.Unmarshal(data, &patch); err != nil {
			t.Fatal(err)
		}
		// Insecure has omitempty, so MergeFrom clears the field when its value becomes false.
		wantPatch := map[string]interface{}{
			"metadata": map[string]interface{}{"uid": string(b.original.UID), "resourceVersion": b.original.ResourceVersion},
			"spec":     map[string]interface{}{"insecure": nil},
		}
		if !reflect.DeepEqual(patch, wantPatch) {
			t.Fatalf("correction patch = %s, want only the cleared insecure field and the original UID and resource version", data)
		}
	}
}

// TestReleaseInsecureSourceIsFatal checks refusal, secure repair and reporting on both controller paths.
func TestReleaseInsecureSourceIsFatal(t *testing.T) {
	for _, path := range []string{sourceCreatePath, sourceUpdatePath, sourceSecurePath} {
		t.Run(path, func(t *testing.T) {
			b := newInsecureSourceTestbed(t, path, kv1alpha1.ConfigSpec{}, OCIRepositoryOptions{})
			policy := b.policyRefusal(t)
			if path == sourceCreatePath {
				sources := &sourcev1.OCIRepositoryList{}
				if err := b.r.List(b.op.ctx, sources); err != nil {
					t.Fatal(err)
				}
				if len(sources.Items) != 0 || b.creates != 0 || len(b.patches) != 0 {
					t.Fatal("refused creation wrote an OCIRepository")
				}
			} else {
				attempts := 1
				if path == sourceSecurePath {
					attempts = 0
				}
				b.checkCorrection(t, attempts, true)
			}
			b.reportPolicyDiagnostic(t, policy)
		})
	}
}

// TestReleaseInsecureSourceRepairFailure checks retryable correction errors followed by a successful fatal refusal.
func TestReleaseInsecureSourceRepairFailure(t *testing.T) {
	resource := schema.GroupResource{Group: sourcev1.GroupVersion.Group, Resource: "ocirepositories"}
	for name, failure := range map[string]error{
		"forbidden": apierrors.NewForbidden(resource, "api-name-canary", errors.New(sourceAPICanary)),
		"conflict":  apierrors.NewConflict(resource, "api-name-canary", errors.New(sourceAPICanary)),
	} {
		t.Run(name, func(t *testing.T) {
			b := newInsecureSourceTestbed(t, sourceUpdatePath, kv1alpha1.ConfigSpec{}, OCIRepositoryOptions{})
			b.patchError = failure
			source, wait, err := b.r.handleOciRepository(b.op)
			if err == nil || err.IsFatal() || err.GetEventReason() != sourcePatchFailureReason || err.Error() != insecureRepairMessage {
				t.Fatalf("failed transport correction must remain retryable: %v", err)
			}
			if source != nil || wait != "" {
				t.Fatal("failed correction returned a source artifact or wait")
			}
			b.checkCorrection(t, 1, false)
			result, retry := b.r.reportError(b.op, err, false)
			if retry == nil || result != (ctrl.Result{}) {
				t.Fatalf("failed correction did not request retry: %+v, %v", result, retry)
			}
			b.expectEvent(t, "Warning "+sourcePatchFailureReason+" "+insecureRepairMessage)
			b.patchError = nil
			policy := b.policyRefusal(t)
			b.checkCorrection(t, 2, true)
			b.reportPolicyDiagnostic(t, policy)
		})
	}
}

// TestReleaseInsecureSourceOwnershipChange rejects a correction if ownership changes after the read.
func TestReleaseInsecureSourceOwnershipChange(t *testing.T) {
	b := newInsecureSourceTestbed(t, sourceUpdatePath, kv1alpha1.ConfigSpec{}, OCIRepositoryOptions{})
	var want *sourcev1.OCIRepository
	conflicted := false
	b.r.Client = interceptor.NewClient(b.r.Client.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*sourcev1.OCIRepository); !ok {
				return c.Patch(ctx, obj, patch, opts...)
			}
			current := &sourcev1.OCIRepository{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
				return err
			}
			current.OwnerReferences[0].UID = types.UID("concurrent-owner-uid")
			current.OwnerReferences[0].Name = "concurrent-owner"
			if err := c.Update(ctx, current); err != nil {
				return err
			}
			want = current.DeepCopy()
			err := c.Patch(ctx, obj, patch, opts...)
			conflicted = apierrors.IsConflict(err)
			return err
		},
	})
	source, wait, err := b.r.handleOciRepository(b.op)
	if !conflicted {
		t.Fatalf("ownership change must conflict before transport correction: %v", err)
	}
	if err == nil || err.IsFatal() || err.GetEventReason() != sourcePatchFailureReason || err.Error() != insecureRepairMessage {
		t.Fatalf("ownership conflict must remain retryable: %v", err)
	}
	if source != nil || wait != "" {
		t.Fatal("ownership conflict returned a source artifact or wait")
	}
	got := fetchedVerificationSource(t, b.r, b.op)
	if !got.Spec.Insecure || !reflect.DeepEqual(got, want) {
		t.Fatalf("correction changed the concurrent owner's source: got %#v, want %#v", got, want)
	}
	if len(b.patches) != 1 || b.creates != 0 {
		t.Fatalf("ownership conflict made %d patches and %d creates, want one refused patch", len(b.patches), b.creates)
	}
	result, retry := b.r.reportError(b.op, err, false)
	if retry == nil || result != (ctrl.Result{}) {
		t.Fatalf("ownership conflict did not request retry: %+v, %v", result, retry)
	}
	b.expectEvent(t, "Warning "+sourcePatchFailureReason+" "+insecureRepairMessage)
}

// TestReleaseInsecureSourceForeignRepository refuses consumption without changing another owner's source.
func TestReleaseInsecureSourceForeignRepository(t *testing.T) {
	for _, ownership := range []string{"unowned", "foreign", "non-controller"} {
		t.Run(ownership, func(t *testing.T) {
			b := newInsecureSourceTestbed(t, sourceUpdatePath, kv1alpha1.ConfigSpec{}, OCIRepositoryOptions{})
			source := fetchedVerificationSource(t, b.r, b.op)
			switch ownership {
			case "unowned":
				source.OwnerReferences = nil
			case "foreign":
				source.OwnerReferences[0].UID = types.UID("foreign-owner-uid")
			default:
				controlled := false
				source.OwnerReferences[0].Controller = &controlled
			}
			if err := b.r.Update(b.op.ctx, source); err != nil {
				t.Fatal(err)
			}
			b.original = fetchedVerificationSource(t, b.r, b.op).DeepCopy()
			got, wait, ownerErr := b.r.handleOciRepository(b.op)
			want := fmt.Sprintf("OCIRepository '%s' exists and is not controlled by this Release", b.op.ociRepositoryName)
			assertSourceOwnerError(t, ownerErr, want, "OCIRepositoryOwner")
			if got != nil || wait != "" {
				t.Fatal("foreign source returned an artifact or wait")
			}
			b.checkCorrection(t, 0, false)
			if ownerErr != nil {
				_, retry := b.r.reportError(b.op, ownerErr, false)
				assertSourceOwnerError(t, retry, want, "OCIRepositoryOwner")
				b.expectEvent(t, "Warning OCIRepositoryOwner "+want)
			}
		})
	}
}

// TestReleaseInsecureSourceRollback checks both opt-outs independently of matching redirect transport.
func TestReleaseInsecureSourceRollback(t *testing.T) {
	for _, path := range []string{sourceCreatePath, sourceUpdatePath} {
		for _, verifyDrop := range []bool{false, true} {
			for _, allowInsecure := range []bool{false, true} {
				for _, route := range []string{verifyNoRedirect, "secure mirror", "insecure mirror"} {
					t.Run(fmt.Sprintf("%s/%s/verifyDrop=%t/allowInsecure=%t", path, route, verifyDrop, allowInsecure), func(t *testing.T) {
						spec := kv1alpha1.ConfigSpec{}
						if route != verifyNoRedirect {
							redirect := verificationRedirect()
							redirect.Insecure = route == "insecure mirror"
							spec.PackageRedirects = []*kv1alpha1.PackageRedirectSpec{redirect}
						}
						b := newInsecureSourceTestbed(t, path, spec, OCIRepositoryOptions{AllowRedirectVerifyDrop: verifyDrop, AllowReleaseInsecureSource: allowInsecure})
						if route == verifyNoRedirect && !allowInsecure {
							policy := b.policyRefusal(t)
							if path == sourceUpdatePath {
								b.checkCorrection(t, 1, true)
							} else if b.creates != 0 || len(b.patches) != 0 {
								t.Fatal("verification rollback allowed a refused source write")
							}
							b.reportPolicyDiagnostic(t, policy)
							return
						}
						_, _, err := b.r.handleOciRepository(b.op)
						if err != nil {
							t.Fatalf("rollback refused direct insecure source: %v", err)
						}
						got := fetchedVerificationSource(t, b.r, b.op)
						pkg := b.op.release.Spec.Package
						want := expectedVerificationSpec(pkg.Repository, pkg.Tag, pkg.OciAddOn, pkg.Interval, pkg.Timeout)
						if route != verifyNoRedirect {
							redirect := spec.PackageRedirects[0]
							want = expectedVerificationSpec(verifyMirrorRepository, verifyMirrorTag, redirect.OciAddOn, redirect.Interval, redirect.Timeout)
							if !verifyDrop {
								want.Verify = pkg.Verify
							}
						}
						if !reflect.DeepEqual(got.Spec, want) {
							t.Fatalf("rollback or redirect changed the wrong settings: got %#v, want %#v", got.Spec, want)
						}
						creates, patches := 1, 0
						if path == sourceUpdatePath {
							creates, patches = 0, 1
						}
						if b.creates != creates || len(b.patches) != patches {
							t.Fatalf("unexpected correction: got %d creates and %d patches", b.creates, len(b.patches))
						}
					})
				}
			}
		}
	}
}

// TestOCIRepositoryAPIFailuresRemainRetryable keeps infrastructure failures separate from policy refusals.
func TestOCIRepositoryAPIFailuresRemainRetryable(t *testing.T) {
	for _, path := range []string{sourceCreatePath, sourceUpdatePath} {
		t.Run(path, func(t *testing.T) {
			b := newInsecureSourceTestbed(t, path, kv1alpha1.ConfigSpec{}, OCIRepositoryOptions{})
			b.op.release.Spec.Package.Insecure = false
			failure := errors.New(sourceAPICanary)
			wantReason := sourceCreateFailureReason
			if path == sourceCreatePath {
				b.createError = failure
			} else {
				b.patchError = failure
				wantReason = sourcePatchFailureReason
			}
			source, _, err := b.r.handleOciRepository(b.op)
			if err == nil || err.IsFatal() || err.GetEventReason() != wantReason {
				t.Fatalf("unrelated API failure became fatal or lost its reason: %v", err)
			}
			if source != nil {
				t.Fatal("API failure returned a source artifact")
			}
			_, retry := b.r.reportError(b.op, err, false)
			if retry == nil {
				t.Fatal("unrelated API failure did not request retry")
			}
		})
	}
}
