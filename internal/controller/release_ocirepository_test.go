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
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/global"
	"kubocd/internal/rolestore"
	"reflect"
	"testing"
	"time"

	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
