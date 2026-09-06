package drill_test

import (
	"errors"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

func TestPlanAcceptsCredentialReferencesWithoutSecretValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  drill.CredentialReference
	}{
		{name: "environment", ref: drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_PASSWORD"}},
		{name: "file", ref: drill.CredentialReference{Provider: drill.CredentialFile, Locator: "/run/secrets/rehearse-password"}},
		{name: "keychain", ref: drill.CredentialReference{Provider: drill.CredentialKeychain, Locator: "rehearse/backup-password"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			plan := validPlan()
			plan.Spec.CredentialReferences = []drill.CredentialReference{test.ref}
			if err := plan.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestPlanRejectsInvalidCredentialReferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  drill.CredentialReference
	}{
		{name: "unknown provider", ref: drill.CredentialReference{Provider: "inline", Locator: "secret"}},
		{name: "invalid environment name", ref: drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "actual password"}},
		{name: "relative file", ref: drill.CredentialReference{Provider: drill.CredentialFile, Locator: "secrets/password"}},
		{name: "invalid keychain locator", ref: drill.CredentialReference{Provider: drill.CredentialKeychain, Locator: "password"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			plan := validPlan()
			plan.Spec.CredentialReferences = []drill.CredentialReference{test.ref}
			if err := plan.Validate(); !errors.Is(err, drill.ErrInvalidPlan) {
				t.Fatalf("Validate error = %v, want ErrInvalidPlan", err)
			}
		})
	}
}

func TestPlanRejectsInvalidPersistedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mutate       func(*drill.Plan)
		wantIdentity bool
	}{
		{name: "missing id", mutate: func(plan *drill.Plan) { plan.ID = "" }},
		{name: "invalid UTF-8 id", mutate: func(plan *drill.Plan) { plan.ID = string([]byte{0xff}) }, wantIdentity: true},
		{name: "missing name", mutate: func(plan *drill.Plan) { plan.Name = " " }},
		{name: "nonpositive version", mutate: func(plan *drill.Plan) { plan.Version = 0 }},
		{name: "missing creation time", mutate: func(plan *drill.Plan) { plan.CreatedAt = time.Time{} }},
		{name: "invalid source kind", mutate: func(plan *drill.Plan) { plan.Spec.SourceKind = "Backup Source" }},
		{name: "invalid target kind", mutate: func(plan *drill.Plan) { plan.Spec.TargetKind = "restore_target" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			plan := validPlan()
			test.mutate(&plan)
			err := plan.Validate()
			if !errors.Is(err, drill.ErrInvalidPlan) {
				t.Fatalf("Validate error = %v, want ErrInvalidPlan", err)
			}
			if test.wantIdentity && !errors.Is(err, drill.ErrInvalidIdentity) {
				t.Fatalf("Validate error = %v, want ErrInvalidIdentity", err)
			}
		})
	}
}

func validPlan() drill.Plan {
	return drill.Plan{
		ID:        "plan-1",
		Name:      "daily drill",
		Version:   1,
		CreatedAt: time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC),
		Spec: drill.PlanSpec{
			SourceKind: "backup-source",
			TargetKind: "restore-target",
		},
	}
}
