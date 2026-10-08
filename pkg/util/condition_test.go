package util

import "testing"

func TestTernary(t *testing.T) {
	t.Run("positive: true picks the first value", func(t *testing.T) {
		if got := Ternary(true, "yes", "no"); got != "yes" {
			t.Errorf("Ternary(true) = %q, want %q", got, "yes")
		}
	})

	t.Run("negative: false picks the second value", func(t *testing.T) {
		if got := Ternary(false, "yes", "no"); got != "no" {
			t.Errorf("Ternary(false) = %q, want %q", got, "no")
		}
		if got := Ternary(2 > 4, 10, 20); got != 20 {
			t.Errorf("Ternary(2 > 4) = %d, want 20", got)
		}
	})

	// Both values are evaluated before the call, so Ternary cannot guard a nil dereference.
	t.Run("negative: arguments are evaluated eagerly", func(t *testing.T) {
		missing := func() *struct{ Name string } { return nil }()
		defer func() {
			if recover() == nil {
				t.Error("Ternary(p != nil, p.Name, ...) did not panic; arguments are no longer eager")
			}
		}()
		_ = Ternary(missing != nil, missing.Name, "fallback")
	})
}
