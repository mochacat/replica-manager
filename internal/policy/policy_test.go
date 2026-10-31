package policy

import (
	"context"
	"testing"
)

func TestLoadDisabledIsNoop(t *testing.T) {
	p, err := Load(false)
	if err != nil {
		t.Fatalf("Load(false) errored: %s", err)
	}
	res, err := p.Evaluate(context.Background(), Input{
		Namespace: "anything",
		Name:      "any",
		Desired:   1000000,
		Current:   1,
	})
	if err != nil {
		t.Fatalf("no-op evaluate errored: %s", err)
	}
	if !res.Allowed {
		t.Errorf("disabled policy should allow everything, got denied: %s", res.Reason)
	}
}

func TestLoadEnabledCompiles(t *testing.T) {
	if _, err := Load(true); err != nil {
		t.Fatalf("embedded policy failed to compile: %s", err)
	}
}

func TestDefaultBounds(t *testing.T) {
	p, err := Load(true)
	if err != nil {
		t.Fatalf("load: %s", err)
	}

	cases := []struct {
		name string
		in   Input
		want bool
	}{
		{"within range allowed", Input{Namespace: "dev", Name: "app", Desired: 10, Current: 10}, true},
		{"at max allowed", Input{Namespace: "dev", Name: "app", Desired: 20, Current: 10}, true},
		{"at min allowed", Input{Namespace: "dev", Name: "app", Desired: 1, Current: 10}, true},
		{"zero denied below min", Input{Namespace: "dev", Name: "app", Desired: 0, Current: 10}, false},
		// The headline scenario: a typo raising 10 to 1000 is denied, never clamped.
		{"1000 with max 20 denied", Input{Namespace: "dev", Name: "app", Desired: 1000, Current: 10}, false},
		{"slightly over max denied", Input{Namespace: "dev", Name: "app", Desired: 21, Current: 10}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Evaluate(context.Background(), tc.in)
			if err != nil {
				t.Fatalf("evaluate: %s", err)
			}
			if res.Allowed != tc.want {
				t.Errorf("allowed=%v want %v (reason=%q)", res.Allowed, tc.want, res.Reason)
			}
			if !res.Allowed && res.Reason == "" {
				t.Errorf("denied result should include a reason")
			}
		})
	}
}

func TestReasonMentionsExceedMax(t *testing.T) {
	p, err := Load(true)
	if err != nil {
		t.Fatalf("load: %s", err)
	}
	res, err := p.Evaluate(context.Background(), Input{Namespace: "dev", Name: "app", Desired: 1000, Current: 10})
	if err != nil {
		t.Fatalf("evaluate: %s", err)
	}
	if res.Allowed {
		t.Fatal("expected 1000 to be denied")
	}
	if res.Reason == "" {
		t.Fatalf("expected a reason for denial, got none")
	}
	t.Logf("denial reason: %s", res.Reason)
}
