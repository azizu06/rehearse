package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCommandExecutorStopsAtOutputLimit(t *testing.T) {
	if os.Getenv("REHEARSE_OUTPUT_HELPER") == "1" {
		fmt.Print(strings.Repeat("x", 8192))
		return
	}
	executor := commandExecutor{
		binary:      os.Args[0],
		environment: append(os.Environ(), "REHEARSE_OUTPUT_HELPER=1"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := executor.run(ctx, 1024, "-test.run=^TestCommandExecutorStopsAtOutputLimit$")
	if !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("run error = %v, want ErrOutputLimitExceeded", err)
	}
	if len(output) != 1024 {
		t.Fatalf("captured output bytes = %d, want 1024", len(output))
	}
}

func TestDockerEnvironmentDoesNotInheritArbitrarySecrets(t *testing.T) {
	t.Setenv("REHEARSE_TEST_SECRET", "must-not-cross-boundary")
	for _, item := range dockerEnvironment() {
		if strings.HasPrefix(item, "REHEARSE_TEST_SECRET=") {
			t.Fatal("Docker subprocess inherited arbitrary environment secret")
		}
	}
}
