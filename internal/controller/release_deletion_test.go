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
	"encoding/json"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/global"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// answeringDeletes answers the deletion of the ClusterConnection named with
// err, without deleting it.
func answeringDeletes(name string, err error) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*kv1alpha1.ClusterConnection); ok && obj.GetName() == name {
					return err
				}
				return c.Delete(ctx, obj, opts...)
			},
		})
	}
}

// refusingClusterConnectionLists refuses the lists of ClusterConnections, quoting the canary.
func refusingClusterConnectionLists(r *ReleaseReconciler) {
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*kv1alpha1.ClusterConnectionList); ok {
				return apierrors.NewForbidden(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "clusterconnections"}, "",
					fmt.Errorf("denied by the policy %q", canaryPassword))
			}
			return c.List(ctx, list, opts...)
		},
	})
}

// deletingThenAnswering deletes the ClusterConnection named, then answers its deletion with err, as when another
// client deleted it meanwhile.
func deletingThenAnswering(name string, err error) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*kv1alpha1.ClusterConnection); ok && obj.GetName() == name {
					if deleteErr := c.Delete(ctx, obj, opts...); deleteErr != nil {
						return deleteErr
					}
					return err
				}
				return c.Delete(ctx, obj, opts...)
			},
		})
	}
}

// clusterConnectionOf is an output ClusterConnection of the Release demo.
func clusterConnectionOf(name string) *kv1alpha1.ClusterConnection {
	return &kv1alpha1.ClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kv1alpha1.ClusterConnectionSpec{Contract: postgresContract,
			ParentRelease: &kv1alpha1.ParentReleaseRef{Namespace: "apps", Name: "demo"}},
	}
}

func TestTheFinalizerOfAReleaseStaysUntilItsClusterConnectionsAreDeleted(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "clusterconnections"},
		"kcd-apps-demo-a", fmt.Errorf("denied by the policy %q", canaryPassword))
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "clusterconnections"},
		"kcd-apps-demo-a")
	cases := map[string]struct {
		options   func(t *testing.T) []func(*ReleaseReconciler)
		wantErr   string
		finalizer bool
		deleted   []string
		left      []string
	}{
		"list refused": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withObjects(t, clusterConnectionOf("kcd-apps-demo-a")), refusingClusterConnectionLists}
		}, "could not list the ClusterConnections of the Release: the API server answered Forbidden, " + objectWithheld, true, nil,
			[]string{"kcd-apps-demo-a"}},
		"first deletion refused": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withObjects(t, clusterConnectionOf("kcd-apps-demo-a"), clusterConnectionOf("kcd-apps-demo-b")),
				answeringDeletes("kcd-apps-demo-a", forbidden)}
		}, "could not delete the ClusterConnections of the Release: the API server answered Forbidden, " + objectWithheld, true,
			[]string{"kcd-apps-demo-b"}, []string{"kcd-apps-demo-a"}},
		"second deletion refused": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withObjects(t, clusterConnectionOf("kcd-apps-demo-a"), clusterConnectionOf("kcd-apps-demo-b")),
				answeringDeletes("kcd-apps-demo-b", forbidden)}
		}, "could not delete the ClusterConnections of the Release: the API server answered Forbidden, " + objectWithheld, true,
			[]string{"kcd-apps-demo-a"}, []string{"kcd-apps-demo-b"}},
		// A control: a ClusterConnection deleted meanwhile is not a failure.
		"ClusterConnection already gone": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withObjects(t, clusterConnectionOf("kcd-apps-demo-a")),
				deletingThenAnswering("kcd-apps-demo-a", notFound)}
		}, "", false, nil, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
				append(c.options(t), deletingTheRelease(t))...)
			got := testbed.reconcile(t)
			for sink, text := range got.sinks() {
				if strings.Contains(text, canaryPassword[:7]) {
					t.Errorf("the %s quotes the answer:\n%s", sink, text)
				}
			}
			if c.wantErr == "" && got.err != nil || c.wantErr != "" && (got.err == nil || got.err.Error() != c.wantErr) {
				t.Fatalf("got the error %v, want %q", got.err, c.wantErr)
			}
			release := &kv1alpha1.Release{}
			if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
				t.Fatal(err)
			}
			if controllerutil.ContainsFinalizer(release, global.FinalizerName) != c.finalizer {
				t.Fatalf("got the finalizers %q, want the one of KuboCD: %t", release.Finalizers, c.finalizer)
			}
			if c.wantErr != "" {
				if release.Status.Phase != kv1alpha1.ReleasePhaseDeleting || release.Status.Message != c.wantErr {
					t.Errorf("got status %s %q, want DELETING %q", release.Status.Phase, release.Status.Message, c.wantErr)
				}
				if !slices.ContainsFunc(got.events, func(e string) bool { return strings.Contains(e, "Warning DeletionError") }) {
					t.Errorf("got events %q, want Warning DeletionError", got.events)
				}
			}
			for _, clusterConnectionName := range c.deleted {
				err := testbed.client.Get(context.Background(), types.NamespacedName{Name: clusterConnectionName}, &kv1alpha1.ClusterConnection{})
				if !apierrors.IsNotFound(err) {
					t.Errorf("%s: got %v, want it deleted", clusterConnectionName, err)
				}
			}
			for _, clusterConnectionName := range c.left {
				if err := testbed.client.Get(context.Background(), types.NamespacedName{Name: clusterConnectionName},
					&kv1alpha1.ClusterConnection{}); err != nil {
					t.Errorf("%s: got %v, want it still there", clusterConnectionName, err)
				}
			}
		})
	}
}

// A control: a folder that cannot be removed does not keep the finalizer.
func TestTheFinalizerOfAReleaseGoesWhenOnlyItsFolderStays(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext, deletingTheRelease(t))
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatalf("got the error %v, want none", got.err)
	}
	release := &kv1alpha1.Release{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
		t.Fatal(err)
	}
	if controllerutil.ContainsFinalizer(release, global.FinalizerName) {
		t.Fatalf("got the finalizers %q, want the one of KuboCD removed", release.Finalizers)
	}
}

// uninstallFinalizer stands for the finalizer of helm-controller: a HelmRelease that holds it stays until its
// uninstallation is over.
const uninstallFinalizer = "test.kubocd.kubotal.io/helm-uninstall"

// childHelmRelease is a HelmRelease of the Release demo, under uninstallation once deleted.
func childHelmRelease(name string, dependsOn ...string) *fluxv2.HelmRelease {
	helmRelease := &fluxv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps", Finalizers: []string{uninstallFinalizer}}}
	for _, dependency := range dependsOn {
		helmRelease.Spec.DependsOn = append(helmRelease.Spec.DependsOn, fluxv2.DependencyReference{Name: dependency})
	}
	ownedByDemo(helmRelease)
	return helmRelease
}

// endUninstall removes the finalizer of helm-controller from the HelmRelease, which goes.
func (b *releaseTestbed) endUninstall(t *testing.T, name string) {
	t.Helper()
	helmRelease := &fluxv2.HelmRelease{}
	if err := b.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: name}, helmRelease); err != nil {
		t.Fatal(err)
	}
	helmRelease.Finalizers = nil
	if err := b.client.Update(context.Background(), helmRelease); err != nil {
		t.Fatal(err)
	}
}

// helmReleaseState is "deleting", "present" or "gone".
func (b *releaseTestbed) helmReleaseState(t *testing.T, name string) string {
	t.Helper()
	helmRelease := &fluxv2.HelmRelease{}
	err := b.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: name}, helmRelease)
	switch {
	case apierrors.IsNotFound(err):
		return "gone"
	case err != nil:
		t.Fatal(err)
	case !helmRelease.DeletionTimestamp.IsZero():
		return "deleting"
	}
	return "present"
}

func (b *releaseTestbed) expectHelmReleases(t *testing.T, step string, want map[string]string) {
	t.Helper()
	for name, state := range want {
		if got := b.helmReleaseState(t, name); got != state {
			t.Errorf("%s: the HelmRelease %s is %s, want %s", step, name, got, state)
		}
	}
}

// expectSources checks whether the Flux sources and the chart folder of the Release demo are there.
func (b *releaseTestbed) expectSources(t *testing.T, step string, there bool) {
	t.Helper()
	for _, source := range []client.Object{&sourcev1.HelmRepository{}, &sourcev1.OCIRepository{}} {
		err := b.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "kcd-demo"}, source)
		if there && err != nil || !there && !apierrors.IsNotFound(err) {
			t.Errorf("%s: got %v for the %T, want it there: %t", step, err, source, there)
		}
	}
	_, err := os.Stat(filepath.Join(b.reconciler.ServerRoot, "hr", "apps", "demo"))
	if there && err != nil || !there && !os.IsNotExist(err) {
		t.Errorf("%s: got %v for the chart folder, want it there: %t", step, err, there)
	}
}

// withRemovableFolder marks the chart folder of the Release demo as one the controller wrote, which it can remove.
func withRemovableFolder(t *testing.T) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		if err := os.WriteFile(filepath.Join(r.ServerRoot, "hr", "apps", "demo", ".transient"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (b *releaseTestbed) release(t *testing.T) *kv1alpha1.Release {
	t.Helper()
	release := &kv1alpha1.Release{}
	if err := b.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
		t.Fatal(err)
	}
	return release
}

func TestADeletedReleaseGoesToThePhaseDeleting(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		withObjects(t, childHelmRelease("demo-main")), deletingTheRelease(t))
	got := testbed.reconcile(t)
	if got.err != nil || got.result.RequeueAfter != DeletionRequeueDelay {
		t.Fatalf("got %v and the error %v, want a requeue after %s", got.result, got.err, DeletionRequeueDelay)
	}
	release := testbed.release(t)
	if release.Status.Phase != kv1alpha1.ReleasePhaseDeleting {
		t.Errorf("got the phase %s, want DELETING", release.Status.Phase)
	}
	ready := apimeta.FindStatusCondition(release.Status.Conditions, kv1alpha1.ReleaseConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "Deleting" || ready.ObservedGeneration != release.Generation {
		t.Errorf("got the Ready condition %+v, want False, Deleting, of the generation %d", ready, release.Generation)
	}
	if release.Status.Message != "Waiting for the deletion of the HelmReleases demo-main" {
		t.Errorf("got the message %q", release.Status.Message)
	}
	if !controllerutil.ContainsFinalizer(release, global.FinalizerName) {
		t.Error("the finalizer of KuboCD is gone while the HelmRelease uninstalls")
	}
	testbed.expectHelmReleases(t, "first pass", map[string]string{"demo-main": "deleting"})
	testbed.expectSources(t, "first pass", true)
}

func TestTheHelmReleasesOfADeletedReleaseGoAfterTheOnesThatDependOnThem(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		withObjects(t, childHelmRelease("demo-main", "demo-oidc-client", "demo-spark-rbac"), childHelmRelease("demo-oidc-client"),
			childHelmRelease("demo-spark-rbac"), childHelmRelease("demo-tools")),
		withRemovableFolder(t), deletingTheRelease(t))
	testbed.reconcile(t)
	testbed.expectHelmReleases(t, "first pass", map[string]string{"demo-main": "deleting", "demo-tools": "deleting",
		"demo-oidc-client": "present", "demo-spark-rbac": "present"})
	// demo-main still uninstalls
	testbed.reconcile(t)
	testbed.expectHelmReleases(t, "second pass", map[string]string{"demo-main": "deleting", "demo-oidc-client": "present",
		"demo-spark-rbac": "present"})
	testbed.endUninstall(t, "demo-main")
	testbed.reconcile(t)
	testbed.expectHelmReleases(t, "demo-main gone, demo-tools uninstalling", map[string]string{"demo-main": "gone",
		"demo-oidc-client": "deleting", "demo-spark-rbac": "deleting", "demo-tools": "deleting"})
	testbed.expectSources(t, "demo-main gone, demo-tools uninstalling", true)
	for _, name := range []string{"demo-oidc-client", "demo-spark-rbac"} {
		testbed.endUninstall(t, name)
	}
	testbed.reconcile(t)
	testbed.expectSources(t, "demo-tools still uninstalling", true)
	if !controllerutil.ContainsFinalizer(testbed.release(t), global.FinalizerName) {
		t.Fatal("the finalizer of KuboCD is gone while demo-tools uninstalls")
	}
	testbed.endUninstall(t, "demo-tools")
	if got := testbed.reconcile(t); got.err != nil || got.result.RequeueAfter != 0 {
		t.Fatalf("last pass: got %v and the error %v", got.result, got.err)
	}
	testbed.expectSources(t, "last pass", false)
	if controllerutil.ContainsFinalizer(testbed.release(t), global.FinalizerName) {
		t.Fatal("the finalizer of KuboCD stays once every child is gone")
	}
}

func TestHelmReleasesThatDependOnEachOtherAreDeletedTogether(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		withObjects(t, childHelmRelease("demo-a", "demo-b"), childHelmRelease("demo-b", "demo-a")), deletingTheRelease(t))
	testbed.reconcile(t)
	testbed.expectHelmReleases(t, "first pass", map[string]string{"demo-a": "deleting", "demo-b": "deleting"})
}

func TestACycleOfHelmReleasesWaitsForItsDependentsAndKeepsWhatItDependsOn(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		withObjects(t, childHelmRelease("demo-front", "demo-a"), childHelmRelease("demo-a", "demo-b"),
			childHelmRelease("demo-b", "demo-a", "demo-c"), childHelmRelease("demo-c")),
		deletingTheRelease(t))
	testbed.reconcile(t)
	testbed.expectHelmReleases(t, "first pass", map[string]string{"demo-front": "deleting", "demo-a": "present",
		"demo-b": "present", "demo-c": "present"})
	testbed.endUninstall(t, "demo-front")
	testbed.reconcile(t)
	testbed.expectHelmReleases(t, "demo-front gone", map[string]string{"demo-a": "deleting", "demo-b": "deleting",
		"demo-c": "present"})
	// demo-b ends first: demo-a, which depended on it, may still need demo-c
	testbed.endUninstall(t, "demo-b")
	testbed.reconcile(t)
	testbed.expectHelmReleases(t, "demo-b gone, demo-a uninstalling", map[string]string{"demo-a": "deleting",
		"demo-c": "present"})
	testbed.endUninstall(t, "demo-a")
	testbed.reconcile(t)
	testbed.expectHelmReleases(t, "cycle gone", map[string]string{"demo-c": "deleting"})
}

func TestTheDeletionOfAReleaseLeavesTheHelmReleasesItDoesNotControl(t *testing.T) {
	foreign := &fluxv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "demo-main", Namespace: "apps"}}
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		withObjects(t, foreign), deletingTheRelease(t))
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatal(got.err)
	}
	testbed.expectHelmReleases(t, "deletion", map[string]string{"demo-main": "present"})
	if controllerutil.ContainsFinalizer(testbed.release(t), global.FinalizerName) {
		t.Fatal("a HelmRelease the Release does not control keeps the finalizer")
	}
}

func TestTheDeletionOfAReleaseReadsChildrenAroundTheCache(t *testing.T) {
	staleCache := func(r *ReleaseReconciler) {
		r.APIReader = r.Client
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*fluxv2.HelmReleaseList); ok {
					return nil
				}
				return c.List(ctx, list, opts...)
			},
		})
	}
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		withObjects(t, childHelmRelease("demo-main")), deletingTheRelease(t), staleCache)
	got := testbed.reconcile(t)
	if got.err != nil || got.result.RequeueAfter != DeletionRequeueDelay {
		t.Fatalf("got %v and error %v, want requeue after %s", got.result, got.err, DeletionRequeueDelay)
	}
	testbed.expectHelmReleases(t, "stale cache", map[string]string{"demo-main": "deleting"})
	if !controllerutil.ContainsFinalizer(testbed.release(t), global.FinalizerName) {
		t.Fatal("finalizer was removed despite HelmRelease visible via APIReader")
	}
}

func TestReconcileDeleteWritesStatusOncePerPassAndReportsErrors(t *testing.T) {
	countStatusUpdates := func(counter *int) func(*ReleaseReconciler) {
		return func(r *ReleaseReconciler) {
			r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					if _, ok := obj.(*kv1alpha1.Release); ok && subResourceName == "status" {
						*counter++
					}
					return c.SubResource(subResourceName).Update(ctx, obj, opts...)
				},
			})
		}
	}
	refuseHelmReleaseList := func(r *ReleaseReconciler) {
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*fluxv2.HelmReleaseList); ok {
					return apierrors.NewForbidden(schema.GroupResource{Group: fluxv2.GroupVersion.Group, Resource: "helmreleases"}, "",
						fmt.Errorf("denied by the policy %q", canaryPassword))
				}
				return c.List(ctx, list, opts...)
			},
		})
	}
	refuseClaimList := func(r *ReleaseReconciler) {
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kv1alpha1.ClaimList); ok {
					return apierrors.NewForbidden(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "claims"}, "",
						fmt.Errorf("denied by the policy %q", canaryPassword))
				}
				return c.List(ctx, list, opts...)
			},
		})
	}

	cases := map[string]struct {
		options       func(t *testing.T) []func(*ReleaseReconciler)
		wantUpdates   int
		wantErr       string
		wantMessage   string
		wantEvent     bool
		wantFinalizer bool
	}{
		"waiting on child HelmRelease writes status once": {
			options: func(t *testing.T) []func(*ReleaseReconciler) {
				return []func(*ReleaseReconciler){withObjects(t, childHelmRelease("demo-main"))}
			},
			wantUpdates:   1,
			wantMessage:   "Waiting for the deletion of the HelmReleases demo-main",
			wantFinalizer: true,
		},
		"step 1 error writes status once and emits DeletionError event": {
			options: func(t *testing.T) []func(*ReleaseReconciler) {
				return []func(*ReleaseReconciler){refuseHelmReleaseList}
			},
			wantUpdates:   1,
			wantErr:       "could not list the HelmReleases of the Release: the API server answered Forbidden, " + objectWithheld,
			wantMessage:   "could not list the HelmReleases of the Release: the API server answered Forbidden, " + objectWithheld,
			wantEvent:     true,
			wantFinalizer: true,
		},
		"step 2 error writes status once and emits DeletionError event": {
			options: func(t *testing.T) []func(*ReleaseReconciler) {
				return []func(*ReleaseReconciler){refuseClaimList}
			},
			wantUpdates:   1,
			wantErr:       "could not list the Claims of the Release: the API server answered Forbidden, " + objectWithheld,
			wantMessage:   "could not list the Claims of the Release: the API server answered Forbidden, " + objectWithheld,
			wantEvent:     true,
			wantFinalizer: true,
		},
		"no remaining children removes finalizer without writing status": {
			options: func(t *testing.T) []func(*ReleaseReconciler) { return nil },
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			statusUpdates := 0
			opts := append(c.options(t), deletingTheRelease(t), countStatusUpdates(&statusUpdates))
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext, opts...)
			got := testbed.reconcile(t)
			for sink, text := range got.sinks() {
				if strings.Contains(text, canaryPassword[:7]) {
					t.Errorf("the %s quotes the answer:\n%s", sink, text)
				}
			}
			if statusUpdates != c.wantUpdates {
				t.Errorf("got %d status updates, want %d", statusUpdates, c.wantUpdates)
			}
			if c.wantErr == "" && got.err != nil || c.wantErr != "" && (got.err == nil || got.err.Error() != c.wantErr) {
				t.Fatalf("got error %v, want %q", got.err, c.wantErr)
			}
			release := testbed.release(t)
			if controllerutil.ContainsFinalizer(release, global.FinalizerName) != c.wantFinalizer {
				t.Errorf("got finalizer %t, want %t", controllerutil.ContainsFinalizer(release, global.FinalizerName), c.wantFinalizer)
			}
			if c.wantMessage != "" {
				if release.Status.Phase != kv1alpha1.ReleasePhaseDeleting || release.Status.Message != c.wantMessage {
					t.Errorf("got status %s %q, want DELETING %q", release.Status.Phase, release.Status.Message, c.wantMessage)
				}
				ready := apimeta.FindStatusCondition(release.Status.Conditions, kv1alpha1.ReleaseConditionReady)
				if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "Deleting" || ready.Message != c.wantMessage {
					t.Errorf("got Ready condition %+v, want False/Deleting/%q", ready, c.wantMessage)
				}
			}
			hasEvent := slices.ContainsFunc(got.events, func(e string) bool {
				return strings.Contains(e, "Warning DeletionError") && strings.Contains(e, c.wantErr)
			})
			if hasEvent != c.wantEvent {
				t.Errorf("got DeletionError event=%t (events=%q), want %t", hasEvent, got.events, c.wantEvent)
			}
		})
	}
}

func TestPresentChildrenUsesFreshObjectWhenChildIsReplacedWithoutOwner(t *testing.T) {
	claim := ownedByDemo(&kv1alpha1.Claim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "demo-db", UID: "claim-old"},
		Spec:       kv1alpha1.ClaimSpec{Contract: postgresContract, Target: kv1alpha1.ClaimTarget{Provisioner: "pg"}},
	})
	conn := ownedByDemo(&kv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "demo-api", UID: "conn-old"},
		Spec:       kv1alpha1.ConnectionSpec{Contract: postgresContract},
	})
	cc := clusterConnectionOf("kcd-apps-demo-api")
	cc.UID = "cc-old"

	// Simulate real Kubernetes JSON decoding into the Get target: when a deleted child is replaced by an unowned
	// object of the same name, OwnerReferences / ParentRelease are omitted from JSON and Go json.Unmarshal does not
	// clear existing fields on a reused struct.
	replaced := map[string]bool{}
	replaceOnDeleteWithJSONGet := func(r *ReleaseReconciler) {
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if err := c.Delete(ctx, obj, opts...); err != nil {
					return err
				}
				key := fmt.Sprintf("%T/%s/%s", obj, obj.GetNamespace(), obj.GetName())
				replaced[key] = true
				var replacement client.Object
				switch obj.(type) {
				case *kv1alpha1.Claim:
					replacement = &kv1alpha1.Claim{
						ObjectMeta: metav1.ObjectMeta{Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: "claim-foreign"},
						Spec:       kv1alpha1.ClaimSpec{Contract: postgresContract, Target: kv1alpha1.ClaimTarget{Provisioner: "pg"}},
					}
				case *kv1alpha1.Connection:
					replacement = &kv1alpha1.Connection{
						ObjectMeta: metav1.ObjectMeta{Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: "conn-foreign"},
						Spec:       kv1alpha1.ConnectionSpec{Contract: postgresContract},
					}
				case *kv1alpha1.ClusterConnection:
					replacement = &kv1alpha1.ClusterConnection{
						ObjectMeta: metav1.ObjectMeta{Name: obj.GetName(), UID: "cc-foreign"},
						Spec:       kv1alpha1.ClusterConnectionSpec{Contract: postgresContract},
					}
				case *sourcev1.HelmRepository:
					replacement = &sourcev1.HelmRepository{
						ObjectMeta: metav1.ObjectMeta{Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: "hr-foreign"},
					}
				case *sourcev1.OCIRepository:
					replacement = &sourcev1.OCIRepository{
						ObjectMeta: metav1.ObjectMeta{Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: "oci-foreign"},
					}
				}
				if replacement != nil {
					return c.Create(ctx, replacement)
				}
				return nil
			},
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				tag := fmt.Sprintf("%T/%s/%s", obj, key.Namespace, key.Name)
				if !replaced[tag] {
					return c.Get(ctx, key, obj, opts...)
				}
				if len(obj.GetOwnerReferences()) != 0 || obj.GetUID() != "" || obj.GetName() != "" {
					t.Errorf("presentChildren passed a dirty %T to Get (name=%q uid=%q owners=%d)",
						obj, obj.GetName(), obj.GetUID(), len(obj.GetOwnerReferences()))
				}
				if ccObj, ok := obj.(*kv1alpha1.ClusterConnection); ok && ccObj.Spec.ParentRelease != nil {
					t.Errorf("presentChildren passed a dirty ClusterConnection to Get (parentRelease=%+v)", ccObj.Spec.ParentRelease)
				}
				tmp := newEmptyObject(obj)
				if err := c.Get(ctx, key, tmp, opts...); err != nil {
					return err
				}
				raw, err := json.Marshal(tmp)
				if err != nil {
					return err
				}
				return json.Unmarshal(raw, obj)
			},
		})
	}

	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		withObjects(t, claim, conn, cc), deletingTheRelease(t), replaceOnDeleteWithJSONGet)
	got := testbed.reconcile(t)
	if got.err != nil || got.result.RequeueAfter != 0 {
		t.Fatalf("got result %v and error %v, want immediate completion once owned children are replaced by foreign ones",
			got.result, got.err)
	}
	if controllerutil.ContainsFinalizer(testbed.release(t), global.FinalizerName) {
		t.Fatal("finalizer stayed because presentChildren mistook unowned replacements for controlled children")
	}
}

func TestDeleteOtherChildrenListsClusterConnectionsOncePerPass(t *testing.T) {
	for name, finalizers := range map[string][]string{
		"deleted immediately": nil,
		"held by finalizer":   {uninstallFinalizer},
	} {
		t.Run(name, func(t *testing.T) {
			cc := clusterConnectionOf("kcd-apps-demo-a")
			cc.Finalizers = finalizers
			ccLists := 0
			countCCLists := func(r *ReleaseReconciler) {
				r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, ok := list.(*kv1alpha1.ClusterConnectionList); ok {
							ccLists++
						}
						return c.List(ctx, list, opts...)
					},
				})
			}
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
				withObjects(t, cc), deletingTheRelease(t), countCCLists)
			got := testbed.reconcile(t)
			if got.err != nil {
				t.Fatalf("got error %v, want none", got.err)
			}
			if ccLists != 1 {
				t.Fatalf("got %d ClusterConnection List calls in one pass, want 1", ccLists)
			}
			wantRequeue := len(finalizers) > 0
			if (got.result.RequeueAfter != 0) != wantRequeue {
				t.Fatalf("got RequeueAfter=%s, want requeue=%t", got.result.RequeueAfter, wantRequeue)
			}
		})
	}
}

func TestRemovingTheFinalizerOfAnAlreadyGoneReleaseSucceeds(t *testing.T) {
	goneOnFinalizerUpdate := func(r *ReleaseReconciler) {
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if rel, ok := obj.(*kv1alpha1.Release); ok && !controllerutil.ContainsFinalizer(rel, global.FinalizerName) {
					return apierrors.NewNotFound(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "releases"}, rel.Name)
				}
				return c.Update(ctx, obj, opts...)
			},
		})
	}
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		deletingTheRelease(t), goneOnFinalizerUpdate)
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatalf("got %v, want NotFound on finalizer removal treated as done", got.err)
	}
}
