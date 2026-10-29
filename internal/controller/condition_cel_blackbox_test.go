//nolint:goconst // Keep expected diagnostics beside each case.
package controller

import (
	"context"
	"errors"
	"fmt"
	"kubocd/internal/kubopackage"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type celObjectClient struct {
	client.Client
	afterGet func()
}

func (c *celObjectClient) RESTMapper() meta.RESTMapper {
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, meta.RESTScopeNamespace)
	return mapper
}

func (c *celObjectClient) Get(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	obj.(*unstructured.Unstructured).Object["data"] = map[string]interface{}{"password": "object-canary"}
	if c.afterGet != nil {
		c.afterGet()
	}
	return nil
}

func celWork() string {
	expr := "true"
	for i := 6; i > 0; i-- {
		expr = fmt.Sprintf("[0,1,2,3,4,5,6,7,8,9].all(x%d, x%d >= 0 && %s)", i, i, expr)
	}
	return expr
}

func celCondition(expr string) *kubopackage.ConditionRendered {
	return &kubopackage.ConditionRendered{Kind: "Secret", Namespace: "target", Name: "db", Cel: expr}
}

func assertCELFailure(t *testing.T, ok bool, msg string, err ReconcileError, want string, fatal bool) {
	t.Helper()
	reason := ""
	if fatal {
		reason = "ConditionError"
	}
	if ok || msg != "" || err == nil || err.Error() != want || err.GetBaseError().Error() != want ||
		err.IsFatal() != fatal || err.GetEventReason() != reason {
		t.Fatalf("got ok=%t message=%q error=%v; want %q fatal=%t reason=%q", ok, msg, err, want, fatal, reason)
	}
}

func assertCELBudgetDiagnostic(t *testing.T, diagnostic string) {
	t.Helper()
	if diagnostic != "condition CEL cost limit exceeded" && diagnostic != "condition CEL evaluation deadline exceeded" {
		t.Fatalf("unexpected budget diagnostic: %q", diagnostic)
	}
}

func TestConditionCELDefaultBudget(t *testing.T) {
	for _, expr := range []string{"'password' in data", "false", celWork()} {
		t.Run(fmt.Sprintf("length=%d", len(expr)), func(t *testing.T) {
			ok, msg, err := CheckCondition(context.Background(), &celObjectClient{}, celCondition(expr))
			if expr == celWork() {
				if err == nil {
					t.Fatal("expected budget exhaustion")
				}
				assertCELBudgetDiagnostic(t, err.Error())
				assertCELFailure(t, ok, msg, err, err.Error(), true)
			} else if err != nil || ok != (expr != "false") || (expr == "false" && !strings.Contains(msg, "the cel expression is false")) {
				t.Fatalf("ordinary condition: ok=%t message=%q error=%v", ok, msg, err)
			}
		})
	}
}

func TestConditionCELCallerCancellation(t *testing.T) {
	t.Run("parent canceled after read", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		c := &celObjectClient{afterGet: func() { cancel(errors.New("cause-canary")) }}
		ok, msg, err := CheckCondition(ctx, c, celCondition("'password' in data"))
		assertCELFailure(t, ok, msg, err, "condition evaluation canceled", false)
	})
	t.Run("expired caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		ok, msg, err := CheckCondition(ctx, &celObjectClient{}, celCondition("'password' in data"))
		assertCELFailure(t, ok, msg, err, "condition CEL evaluation deadline exceeded", true)
	})
}
