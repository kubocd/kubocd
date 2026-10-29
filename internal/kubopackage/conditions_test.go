package kubopackage

import "testing"

func TestNullConditionIsRefused(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("null condition panicked instead of being refused: %v", p)
		}
	}()
	_, err := renderScopeConditions(t, "- null", "target")
	if err == nil || err.Error() != "condition[0]: "+scopePolicyMessage {
		t.Fatalf("got %v, want indexed policy refusal", err)
	}
}
