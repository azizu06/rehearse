package restic

import (
	"context"
	"errors"
	"testing"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/source"
)

type funcCredentialResolver func(context.Context, drill.CredentialReference) ([]byte, error)

func (resolve funcCredentialResolver) Resolve(ctx context.Context, reference drill.CredentialReference) ([]byte, error) {
	return resolve(ctx, reference)
}

func TestResolveS3Credential(t *testing.T) {
	t.Parallel()

	resolveErr := errors.New("resolver unavailable")

	tests := []struct {
		name     string
		resolve  func(context.Context, drill.CredentialReference) ([]byte, error)
		ctx      func() (context.Context, context.CancelFunc)
		wantKind source.FailureKind
	}{
		{
			name: "resolver error without context cancellation is an authentication failure",
			resolve: func(context.Context, drill.CredentialReference) ([]byte, error) {
				return nil, resolveErr
			},
			wantKind: source.FailureAuthentication,
		},
		{
			name: "resolver error after context cancellation delegates to contextFailure",
			resolve: func(context.Context, drill.CredentialReference) ([]byte, error) {
				return nil, resolveErr
			},
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantKind: source.FailureCancelled,
		},
		{
			name: "empty resolved value is an authentication failure",
			resolve: func(context.Context, drill.CredentialReference) ([]byte, error) {
				return []byte(""), nil
			},
			wantKind: source.FailureAuthentication,
		},
		{
			name: "resolved value containing a newline is an authentication failure",
			resolve: func(context.Context, drill.CredentialReference) ([]byte, error) {
				return []byte("secret\nkey"), nil
			},
			wantKind: source.FailureAuthentication,
		},
		{
			name: "resolved value containing a NUL byte is an authentication failure",
			resolve: func(context.Context, drill.CredentialReference) ([]byte, error) {
				return []byte("secret\x00key"), nil
			},
			wantKind: source.FailureAuthentication,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if test.ctx != nil {
				var cancel context.CancelFunc
				ctx, cancel = test.ctx()
				defer cancel()
			}

			adapter := &Adapter{resolver: funcCredentialResolver(test.resolve)}
			_, err := adapter.resolveS3Credential(ctx, "acquire", drill.CredentialReference{})

			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != test.wantKind {
				t.Fatalf("resolveS3Credential() error = %v, want kind %q", err, test.wantKind)
			}
		})
	}
}

func TestResolveS3CredentialSuccess(t *testing.T) {
	t.Parallel()

	adapter := &Adapter{resolver: funcCredentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
		return []byte("valid-secret"), nil
	})}

	value, err := adapter.resolveS3Credential(context.Background(), "acquire", drill.CredentialReference{})
	if err != nil {
		t.Fatalf("resolveS3Credential() error = %v, want nil", err)
	}
	if string(value) != "valid-secret" {
		t.Fatalf("resolveS3Credential() = %q, want %q", value, "valid-secret")
	}
}
