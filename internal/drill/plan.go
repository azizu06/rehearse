// Package drill defines Rehearse's vendor-independent plan and run domain.
package drill

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azizu06/rehearse/internal/probe"
)

// ErrInvalidPlan identifies a plan that cannot safely enter the journal.
var ErrInvalidPlan = errors.New("invalid drill plan")

var ErrInvalidIdentity = errors.New("invalid text identity")

// CredentialProvider identifies a supported out-of-band secret lookup.
type CredentialProvider string

const (
	CredentialEnvironment CredentialProvider = "environment"
	CredentialFile        CredentialProvider = "file"
	CredentialKeychain    CredentialProvider = "keychain"
)

// CredentialReference points to a secret held outside SQLite. It intentionally
// has no field capable of carrying the secret value itself.
type CredentialReference struct {
	Provider CredentialProvider `json:"provider"`
	Locator  string             `json:"locator"`
}

// PlanSpec is the persistable, secret-free portion of a drill plan. Adapter
// value-bearing configuration belongs behind configured adapter boundaries,
// not in this journal schema.
type PlanSpec struct {
	SourceKind           string                `json:"source_kind"`
	TargetKind           string                `json:"target_kind"`
	CredentialReferences []CredentialReference `json:"credential_references,omitempty"`
	ProbeConfig          probe.Config          `json:"probe_config,omitempty"`
}

// Plan is one immutable version of a drill definition.
type Plan struct {
	ID        string
	Name      string
	Version   int64
	Spec      PlanSpec
	CreatedAt time.Time
}

var (
	identifierPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	environmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	keychainPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}/[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// Validate enforces the persistence boundary before any plan reaches SQLite.
func (plan Plan) Validate() error {
	switch {
	case strings.TrimSpace(plan.ID) == "":
		return fmt.Errorf("%w: id is required", ErrInvalidPlan)
	case !utf8.ValidString(plan.ID):
		return fmt.Errorf("%w: id: %w", ErrInvalidPlan, ErrInvalidIdentity)
	case strings.TrimSpace(plan.Name) == "":
		return fmt.Errorf("%w: name is required", ErrInvalidPlan)
	case plan.Version < 1:
		return fmt.Errorf("%w: version must be positive", ErrInvalidPlan)
	case plan.CreatedAt.IsZero():
		return fmt.Errorf("%w: created time is required", ErrInvalidPlan)
	case !identifierPattern.MatchString(plan.Spec.SourceKind):
		return fmt.Errorf("%w: invalid source kind", ErrInvalidPlan)
	case !identifierPattern.MatchString(plan.Spec.TargetKind):
		return fmt.Errorf("%w: invalid target kind", ErrInvalidPlan)
	}

	for index, reference := range plan.Spec.CredentialReferences {
		if err := reference.validate(); err != nil {
			return fmt.Errorf("%w: credential reference %d: %v", ErrInvalidPlan, index, err)
		}
	}
	if !plan.Spec.ProbeConfig.IsZero() {
		if err := plan.Spec.ProbeConfig.Validate(); err != nil {
			return fmt.Errorf("%w: probe config: %v", ErrInvalidPlan, err)
		}
	}
	return nil
}

func (reference CredentialReference) validate() error {
	switch reference.Provider {
	case CredentialEnvironment:
		if !environmentPattern.MatchString(reference.Locator) {
			return errors.New("environment locator must be a variable name")
		}
	case CredentialFile:
		if !filepath.IsAbs(reference.Locator) || filepath.Clean(reference.Locator) != reference.Locator {
			return errors.New("file locator must be a clean absolute path")
		}
	case CredentialKeychain:
		if !keychainPattern.MatchString(reference.Locator) {
			return errors.New("keychain locator must be service/account")
		}
	default:
		return errors.New("unsupported credential provider")
	}
	return nil
}

// Validate checks that a credential reference cannot carry an inline value and
// uses a locator shape supported by its provider.
func (reference CredentialReference) Validate() error {
	return reference.validate()
}
