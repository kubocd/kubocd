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
	kv1alpha1 "kubocd/api/v1alpha1"
	"strings"
	"testing"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// claimPackage claims its database, and optionally a database of examples.
const claimPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
schema:
  parameters:
    properties:
      db:
        type: claim
        contract: database-v1
        required: true
        requirements: {engines: [postgresql]}
      examples:
        type: claim
        contract: database-v1
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

func (b *releaseTestbed) phase(t *testing.T) kv1alpha1.ReleasePhase {
	t.Helper()
	release := &kv1alpha1.Release{}
	if err := b.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
		t.Fatal(err)
	}
	return release.Status.Phase
}

func (b *releaseTestbed) claim(t *testing.T, name string) *kv1alpha1.Claim {
	t.Helper()
	claim := &kv1alpha1.Claim{}
	err := b.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: name}, claim)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func (b *releaseTestbed) setParameters(t *testing.T, parameters string) {
	t.Helper()
	release := &kv1alpha1.Release{}
	if err := b.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
		t.Fatal(err)
	}
	release.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(parameters)}
	if err := b.client.Update(context.Background(), release); err != nil {
		t.Fatal(err)
	}
}

func TestAClaimParameterWaitsForItsProvisionerThenReadsTheBoundConnection(t *testing.T) {
	bed := newReleaseTestbed(t, claimPackage, `{"db": {"provisioner": "postgresql"}}`, "")
	got := bed.reconcile(t)
	got.mustReportEvent(t, "claim 'demo-db': waiting for provisioner 'postgresql'")
	if bed.phase(t) != kv1alpha1.ReleasePhaseWaitClaims {
		t.Fatalf("got phase %q", bed.phase(t))
	}
	claim := bed.claim(t, "demo-db")
	if claim == nil || !metav1.IsControlledBy(claim, &kv1alpha1.Release{ObjectMeta: metav1.ObjectMeta{UID: "demo"}}) ||
		claim.Spec.Contract != "database-v1" || claim.Spec.Target != (kv1alpha1.ClaimTarget{Provisioner: "postgresql"}) ||
		string(claim.Spec.Requirements.Raw) != `{"engines":["postgresql"]}` {
		t.Fatalf("got claim %+v", claim)
	}
	if bed.claim(t, "demo-examples") != nil {
		t.Fatal("a claim parameter left unset gives no claim")
	}

	// The provisioner unbinds, then binds the claim to the connection it creates
	claim.Status = kv1alpha1.ClaimStatus{Phase: kv1alpha1.ClaimPhaseUnbound, Message: "requirement 'engines' is not met",
		ObservedGeneration: claim.Generation}
	if err := bed.client.Update(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	bed.reconcile(t).mustReportEvent(t, "claim 'demo-db': requirement 'engines' is not met")
	if err := bed.client.Create(context.Background(), readyConnection("demo-db", "database-v1", `{"host": "pg.demo"}`)); err != nil {
		t.Fatal(err)
	}
	claim = bed.claim(t, "demo-db")
	claim.Status = kv1alpha1.ClaimStatus{Phase: kv1alpha1.ClaimPhaseBound, Connection: "demo-db", ObservedGeneration: claim.Generation}
	if err := bed.client.Update(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	bed.reconcile(t)
	helmRelease := &fluxv2.HelmRelease{}
	if err := bed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo-main"}, helmRelease); err != nil {
		t.Fatalf("no HelmRelease, phase %q: %v", bed.phase(t), err)
	}
	if !strings.Contains(string(helmRelease.Spec.Values.Raw), `"db":"pg.demo"`) {
		t.Fatalf("got values %s", helmRelease.Spec.Values.Raw)
	}
}

func TestAStaleBindingOfAClaimIsNotUsed(t *testing.T) {
	bed := newReleaseTestbed(t, claimPackage, `{"db": {"provisioner": "postgresql"}}`, "")
	bed.reconcile(t)
	claim := bed.claim(t, "demo-db")
	claim.Status = kv1alpha1.ClaimStatus{Phase: kv1alpha1.ClaimPhaseBound, Connection: "demo-db", ObservedGeneration: claim.Generation - 1}
	if err := bed.client.Update(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	bed.reconcile(t).mustReportEvent(t, "claim 'demo-db': waiting for provisioner 'postgresql'")
}

func TestAClaimWhoseTargetChangesIsReplaced(t *testing.T) {
	bed := newReleaseTestbed(t, claimPackage, `{"db": {"provisioner": "postgresql"}}`, "")
	bed.reconcile(t)
	bed.setParameters(t, `{"db": {"connection": "ext-pg"}}`)
	bed.reconcile(t).mustReportEvent(t, "claim 'demo-db': being replaced")
	if bed.claim(t, "demo-db") != nil {
		t.Fatal("the claim on the provisioner is not deleted")
	}
	bed.reconcile(t).mustReportEvent(t, "claim 'demo-db': not checked, as the connections feature gate is off")
	if claim := bed.claim(t, "demo-db"); claim == nil || claim.Spec.Target != (kv1alpha1.ClaimTarget{Connection: "ext-pg"}) {
		t.Fatalf("got claim %+v", claim)
	}
}

func TestAClaimThatTheReleaseDoesNotControlIsLeftAsIs(t *testing.T) {
	foreign := &kv1alpha1.Claim{ObjectMeta: metav1.ObjectMeta{Name: "demo-db", Namespace: "apps"},
		Spec: kv1alpha1.ClaimSpec{Contract: "s3-v1", Target: kv1alpha1.ClaimTarget{Connection: "bucket"}}}
	got := reconcileRelease(t, claimPackage, `{"db": {"provisioner": "postgresql"}}`, "", withObjects(t, foreign))
	got.mustReport(t, "the Claim 'demo-db' exists and the Release does not control it")
}

func TestTheOrphanClaimsOfAReleaseAreDeleted(t *testing.T) {
	bed := newReleaseTestbed(t, claimPackage, "", "")
	release := &kv1alpha1.Release{}
	if err := bed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
		t.Fatal(err)
	}
	claim := func(name string, owned bool) client.Object {
		object := &kv1alpha1.Claim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps"},
			Spec: kv1alpha1.ClaimSpec{Contract: "database-v1", Target: kv1alpha1.ClaimTarget{Provisioner: "postgresql"}}}
		if owned {
			ownedByDemo(object)
		}
		return object
	}
	for _, object := range []client.Object{claim("demo-db", true), claim("demo-examples", true), claim("other", false)} {
		if err := bed.client.Create(context.Background(), object); err != nil {
			t.Fatal(err)
		}
	}
	op := &releaseOperation{ctx: context.Background(), logger: logr.Discard(), release: release,
		claimNames: map[string]struct{}{"demo-db": {}}}
	if err := bed.reconciler.deleteOrphanClaims(op); err != nil {
		t.Fatal(err)
	}
	if bed.claim(t, "demo-db") == nil || bed.claim(t, "demo-examples") != nil || bed.claim(t, "other") == nil {
		t.Fatal("only the claim the Release controls and no longer declares must be deleted")
	}
}

func TestSameRequirements(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"", "", true},
		{"", "{}", true},
		{`{"a":1,"b":[1,2]}`, `{"b":[1,2],"a":1}`, true},
		{`{"a":1}`, `{"a":2}`, false},
		{`{"a":1}`, "", false},
	} {
		toJSON := func(raw string) *apiextensionsv1.JSON {
			if raw == "" {
				return nil
			}
			return &apiextensionsv1.JSON{Raw: []byte(raw)}
		}
		if got := sameRequirements(toJSON(c.a), toJSON(c.b)); got != c.want {
			t.Errorf("sameRequirements(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// mustReportEvent fails the test unless the status and an event hold want.
func (r reconciledRelease) mustReportEvent(t *testing.T, want string) {
	t.Helper()
	if !strings.Contains(r.status, want) {
		t.Fatalf("got status %q, want one containing %q", r.status, want)
	}
	for _, event := range r.events {
		if strings.Contains(event, want) {
			return
		}
	}
	t.Fatalf("got events %q, want one containing %q", r.events, want)
}
