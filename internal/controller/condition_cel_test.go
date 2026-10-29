//nolint:goconst // Keep expression cases and expected errors readable.
package controller

import (
	"context"
	"errors"
	"fmt"
	kv1 "kubocd/api/v1alpha1"
	"strings"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/interpreter"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestConditionCELMeasuredCost(t *testing.T) {
	env, err := cel.NewEnv(cel.Variable("data", cel.DynType))
	if err != nil {
		t.Fatal(err)
	}
	for _, expr := range []string{"'password' in data", celWork()} {
		ast, issues := env.Compile(expr)
		if issues.Err() != nil {
			t.Fatal(issues.Err())
		}
		program, err := env.Program(ast, cel.EvalOptions(cel.OptTrackCost), cel.InterruptCheckFrequency(100))
		if err != nil {
			t.Fatal(err)
		}
		value, details, err := program.ContextEval(context.Background(), map[string]interface{}{
			"data": map[string]interface{}{"password": "value"},
		})
		if err != nil || value.Value() != true || details.ActualCost() == nil {
			t.Fatalf("evaluation: %v %v %v", value, details, err)
		}
		cost := *details.ActualCost()
		t.Logf("cel-go v0.31.0: expression length=%d actual cost=%d default=%d", len(expr), cost, DefaultConditionLimits.CostLimit)
		if expr == "'password' in data" {
			if cost > 10 || cost >= uint64(DefaultConditionLimits.CostLimit)/1000 {
				t.Fatalf("membership cost unexpectedly large: %d", cost)
			}
		} else if cost <= uint64(DefaultConditionLimits.CostLimit) {
			t.Fatalf("fixture does not exceed default: %d", cost)
		}
	}
}

func TestConditionCELConfiguredLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits ConditionLimits
		want   string
	}{
		{"ordinary budget", ConditionLimits{CostLimit: 1000000, Timeout: 0}, "condition CEL cost limit exceeded"},
		{"small budget", ConditionLimits{CostLimit: 1}, "condition CEL cost limit exceeded"},
		{"cost disabled", ConditionLimits{Timeout: 10 * time.Second}, ""},
		{"both disabled", ConditionLimits{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, msg, err := CheckConditionWithLimits(context.Background(), &celObjectClient{}, celCondition(celWork()), tc.limits)
			if tc.want != "" {
				assertCELFailure(t, ok, msg, err, tc.want, true)
			} else if !ok || msg != "" || err != nil {
				t.Fatalf("disabled limits: ok=%t message=%q error=%v", ok, msg, err)
			}
		})
	}
	for _, limits := range []ConditionLimits{{CostLimit: -1}, {Timeout: -1}} {
		if err := limits.Validate(); err == nil {
			t.Fatalf("negative limits accepted: %+v", limits)
		}
	}
}

type celObservedContext struct {
	context.Context
	deadlineCalls int
	doneCalls     int
}

func (c *celObservedContext) Deadline() (time.Time, bool) {
	c.deadlineCalls++
	return c.Context.Deadline()
}
func (c *celObservedContext) Done() <-chan struct{} {
	c.doneCalls++
	return c.Context.Done()
}

func TestConditionCELTimeoutContext(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Second} {
		ctx := &celObservedContext{Context: context.Background()}
		ok, msg, err := CheckConditionWithLimits(ctx, &celObjectClient{}, celCondition("'password' in data"), ConditionLimits{Timeout: timeout})
		if !ok || msg != "" || err != nil {
			t.Fatalf("ordinary condition: %t %q %v", ok, msg, err)
		}
		if (ctx.deadlineCalls > 0) != (timeout > 0) {
			t.Errorf("timeout=%v queried parent Deadline %d times", timeout, ctx.deadlineCalls)
		}
		if ctx.doneCalls == 0 {
			t.Error("evaluation ignored caller context")
		}
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	ok, msg, err := CheckConditionWithLimits(ctx, &celObjectClient{}, celCondition("true"), ConditionLimits{})
	assertCELFailure(t, ok, msg, err, "condition CEL evaluation deadline exceeded", true)
}

func TestConditionCELRunningDeadline(t *testing.T) {
	list := &deadlineCELList{Lister: celtypes.NewDynamicList(celtypes.DefaultTypeAdapter, make([]int, 1000))}
	ok, msg, err := evalConditionCel(context.Background(), celCondition("data.all(x, x >= 0)"),
		map[string]interface{}{"data": list}, ConditionLimits{Timeout: 100 * time.Millisecond})
	assertCELFailure(t, ok, msg, err, "condition CEL evaluation deadline exceeded", true)
	if list.reads == 0 || list.reads > 200 {
		t.Fatalf("evaluation did not stop cooperatively after the child deadline: %d of 1000 reads", list.reads)
	}
}

type deadlineCELList struct {
	traits.Lister
	reads int
}

func (l *deadlineCELList) Iterator() traits.Iterator {
	return &deadlineCELIterator{Iterator: l.Lister.Iterator(), list: l}
}

type deadlineCELIterator struct {
	traits.Iterator
	list *deadlineCELList
}

func (i *deadlineCELIterator) Next() ref.Val {
	i.list.reads++
	if i.list.reads == 1 {
		time.Sleep(200 * time.Millisecond)
	}
	return i.Iterator.Next()
}

type cancelingCELList struct {
	traits.Lister
	reads  int
	cancel context.CancelCauseFunc
}

func (l *cancelingCELList) Iterator() traits.Iterator {
	return &cancelingCELIterator{Iterator: l.Lister.Iterator(), list: l}
}

type cancelingCELIterator struct {
	traits.Iterator
	list *cancelingCELList
}

func (i *cancelingCELIterator) Next() ref.Val {
	i.list.reads++
	if i.list.reads == 201 {
		i.list.cancel(errors.New("cause-canary"))
	}
	return i.Iterator.Next()
}

func TestConditionCELRunningCancellation(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	list := &cancelingCELList{Lister: celtypes.NewDynamicList(celtypes.DefaultTypeAdapter, make([]int, 1000)), cancel: cancel}
	ok, msg, err := evalConditionCel(ctx, celCondition("data.all(x, x >= 0)"), map[string]interface{}{"data": list}, ConditionLimits{})
	assertCELFailure(t, ok, msg, err, "condition evaluation canceled", false)
	if list.reads < 201 || list.reads > 400 {
		t.Fatalf("evaluation did not stop cooperatively after cancellation: %d reads", list.reads)
	}
}

func TestConditionCELFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  string
		fatal bool
	}{
		{"cost", fmt.Errorf("wrapped: %w", interpreter.EvalCancelledError{Cause: interpreter.CostLimitExceeded, Message: "canary"}), "condition CEL cost limit exceeded", true},
		{"cancel", interpreter.EvalCancelledError{Cause: interpreter.ContextCancelled, Message: "canary"}, "condition evaluation canceled", false},
		{"interrupt", fmt.Errorf("wrapped: %w", interpreter.InterruptError{}), "condition evaluation canceled", false},
		{"ordinary", errors.New("operation cancelled: actual cost limit exceeded"), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := conditionEvaluationError(context.Background(), context.Background(), tc.cause)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("classified by message: %v", err)
				}
				return
			}
			assertCELFailure(t, false, "", err, tc.want, tc.fatal)
		})
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	expired, done := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer done()
	err := conditionEvaluationError(parent, expired, interpreter.EvalCancelledError{Cause: interpreter.CostLimitExceeded})
	assertCELFailure(t, false, "", err, "condition evaluation canceled", false)
}

func TestReleaseCELFailuresAreDefinitive(t *testing.T) {
	for _, tc := range []struct {
		name, expr, want string
		limits           *ConditionLimits
		cancel           bool
		defaultBudget    bool
	}{
		{name: "defaults", expr: celWork(), defaultBudget: true},
		{name: "deadline", expr: celWork(), limits: &ConditionLimits{Timeout: time.Nanosecond}, want: "condition CEL evaluation deadline exceeded"},
		{name: "custom cost", expr: "'password' in data", limits: &ConditionLimits{CostLimit: 1}, want: "condition CEL cost limit exceeded"},
		{name: "disabled", expr: celWork(), limits: &ConditionLimits{}},
		{name: "false waits", expr: "false"},
		{name: "shutdown", expr: celWork(), want: "condition evaluation canceled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			manifest := fmt.Sprintf(afterRenderPackage, fmt.Sprintf("conditions: |\n  - kind: Secret\n    name: db\n    cel: %q", tc.expr), "")
			bed := newReleaseTestbed(t, manifest, "", "", func(r *ReleaseReconciler) {
				r.ConditionLimits = tc.limits
				r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if u, ok := obj.(*unstructured.Unstructured); ok {
						u.Object["data"] = map[string]interface{}{"password": "object-canary"}
						if tc.cancel {
							cancel()
						}
						return nil
					}
					return c.Get(ctx, key, obj, opts...)
				}})
			})
			key := types.NamespacedName{Namespace: "apps", Name: "demo"}
			result, err := bed.reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			release := &kv1.Release{}
			if getErr := bed.client.Get(context.Background(), key, release); getErr != nil {
				t.Fatal(getErr)
			}
			var events []string
			for len(bed.recorder.Events) > 0 {
				events = append(events, <-bed.recorder.Events)
			}
			if tc.want != "" || tc.defaultBudget {
				want := "condition[0]: " + tc.want
				if tc.defaultBudget {
					diagnostic := strings.TrimPrefix(release.Status.Message, "condition[0]: ")
					assertCELBudgetDiagnostic(t, diagnostic)
					want = "condition[0]: " + diagnostic
				}
				if release.Status.Message != want || release.Status.Phase != kv1.ReleasePhaseError || !result.IsZero() || (err != nil) != tc.cancel {
					t.Fatalf("result=%+v err=%v status=%+v", result, err, release.Status)
				}
				if tc.cancel {
					if strings.Contains(strings.Join(events, "\n"), "ConditionError") {
						t.Fatalf("shutdown warning: %v", events)
					}
				} else if !strings.Contains(strings.Join(events, "\n"), "Warning ConditionError "+want) {
					t.Fatalf("missing warning: %v", events)
				}
			} else if tc.expr == "false" {
				if err != nil || result.RequeueAfter != 5*time.Second || release.Status.Phase != kv1.ReleasePhaseWaitConditions {
					t.Fatalf("false did not wait: %+v %v %+v", result, err, release.Status)
				}
			} else if err != nil || release.Status.Phase == kv1.ReleasePhaseError || release.Status.Phase == kv1.ReleasePhaseWaitConditions {
				t.Fatalf("explicit zero limits not propagated: %+v %v %+v", result, err, release.Status)
			}
		})
	}
}
