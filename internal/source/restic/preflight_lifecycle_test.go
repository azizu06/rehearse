package restic

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
)

type lifecycleCredentialResolver struct{}

func (lifecycleCredentialResolver) Resolve(context.Context, drill.CredentialReference) ([]byte, error) {
	return []byte("fixture-password"), nil
}

type blockingLifecycleResolver struct {
	started chan struct{}
	release chan struct{}
}

func (resolver blockingLifecycleResolver) Resolve(context.Context, drill.CredentialReference) ([]byte, error) {
	close(resolver.started)
	<-resolver.release
	return []byte("fixture-password"), nil
}

func TestPreflightWaitsForVerifiedOperationLease(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	binary := filepath.Join(t.TempDir(), "restic")
	if err := os.WriteFile(binary, []byte(`#!/bin/sh
case " $* " in
  *" version "*) printf '%s\n' '{"message_type":"version","version":"0.19.1"}' ;;
  *" snapshots "*) printf '%s\n' '[]' ;;
  *) exit 90 ;;
esac
`), 0o700); err != nil {
		t.Fatalf("write fake restic: %v", err)
	}
	adapter, err := New(Config{
		Binary:            binary,
		Repository:        Repository{Kind: RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: t.TempDir(),
	}, blockingLifecycleResolver{started: started, release: release})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if _, err := adapter.Capabilities(context.Background()); err != nil {
		t.Fatalf("initial preflight: %v", err)
	}

	listDone := make(chan error, 1)
	go func() {
		_, err := adapter.ListRecoveryPoints(context.Background())
		listDone <- err
	}()
	receiveLifecycleValue(t, started)

	preflightDone := make(chan error, 1)
	go func() {
		_, err := adapter.Capabilities(context.Background())
		preflightDone <- err
	}()
	select {
	case err := <-preflightDone:
		t.Fatalf("preflight bypassed active verified operation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := receiveLifecycleValue(t, listDone); err != nil {
		t.Fatalf("list recovery points: %v", err)
	}
	if err := receiveLifecycleValue(t, preflightDone); err != nil {
		t.Fatalf("preflight after operation: %v", err)
	}
}

func TestPreflightGateReleaseWindowRemainsFailClosed(t *testing.T) {
	adapter := newLifecycleTestAdapter(t)
	hookEntered := make(chan int, 2)
	continueHook := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var hookCalls atomic.Int32
	adapter.afterGateRelease = func() {
		index := int(hookCalls.Add(1) - 1)
		hookEntered <- index
		<-continueHook[index]
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := adapter.Capabilities(context.Background())
		firstDone <- err
	}()
	if index := receiveLifecycleValue(t, hookEntered); index != 0 {
		t.Fatalf("first hook index = %d, want 0", index)
	}
	if err := adapter.requirePreflight("list"); err == nil {
		t.Fatal("operation allowed while first preflight awaited completion")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := adapter.Capabilities(context.Background())
		secondDone <- err
	}()
	if index := receiveLifecycleValue(t, hookEntered); index != 1 {
		t.Fatalf("second hook index = %d, want 1", index)
	}
	if err := adapter.requirePreflight("list"); err == nil {
		t.Fatal("operation allowed while overlapping preflights awaited completion")
	}

	close(continueHook[1])
	if err := receiveLifecycleValue(t, secondDone); err != nil {
		t.Fatalf("second preflight: %v", err)
	}
	if err := adapter.requirePreflight("list"); err == nil {
		t.Fatal("operation allowed while first preflight remained pending")
	}

	close(continueHook[0])
	if err := receiveLifecycleValue(t, firstDone); err != nil {
		t.Fatalf("first preflight: %v", err)
	}
	if err := adapter.requirePreflight("list"); err != nil {
		t.Fatalf("operation rejected after successful batch quiesced: %v", err)
	}
}

func TestPreflightBatchFailureWinsInEitherCompletionOrder(t *testing.T) {
	tests := []struct {
		name       string
		completion []bool
	}{
		{name: "success then failure", completion: []bool{true, false}},
		{name: "failure then success", completion: []bool{false, true}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &Adapter{}
			adapter.beginPreflight()
			adapter.beginPreflight()
			adapter.completePreflight(test.completion[0])
			if err := adapter.requirePreflight("list"); err == nil {
				t.Fatal("operation allowed before overlapping batch quiesced")
			}
			adapter.completePreflight(test.completion[1])
			if err := adapter.requirePreflight("list"); err == nil {
				t.Fatal("operation allowed after a mixed-result batch")
			}
			adapter.beginPreflight()
			adapter.completePreflight(true)
			if err := adapter.requirePreflight("list"); err != nil {
				t.Fatalf("operation rejected after later successful batch: %v", err)
			}
		})
	}
}

func newLifecycleTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "restic")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' '{\"message_type\":\"version\",\"version\":\"0.19.1\"}'\n"), 0o700); err != nil {
		t.Fatalf("write fake restic: %v", err)
	}
	adapter, err := New(Config{
		Binary:            binary,
		Repository:        Repository{Kind: RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: t.TempDir(),
	}, lifecycleCredentialResolver{})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	return adapter
}

func receiveLifecycleValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for preflight lifecycle")
		var zero T
		return zero
	}
}
