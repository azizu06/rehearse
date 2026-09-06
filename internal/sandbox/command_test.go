package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

func TestCommandExecutorReapsProcessAtDeadline(t *testing.T) {
	if os.Getenv("REHEARSE_DEADLINE_HELPER") == "1" {
		if err := os.WriteFile(os.Getenv("REHEARSE_DEADLINE_MARKER"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(2)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	marker := filepath.Join(t.TempDir(), "started")
	executor := commandExecutor{
		binary:      os.Args[0],
		environment: append(os.Environ(), "REHEARSE_DEADLINE_HELPER=1", "REHEARSE_DEADLINE_MARKER="+marker),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := executor.run(ctx, 1024, "-test.run=^TestCommandExecutorReapsProcessAtDeadline$")
		result <- err
	}()
	var pid int
	startDeadline := time.Now().Add(750 * time.Millisecond)
	for pid == 0 {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, err = strconv.Atoi(string(data))
			if err != nil {
				t.Fatalf("decode child process marker: %v", err)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read child process marker: %v", err)
		}
		select {
		case err := <-result:
			t.Fatalf("command returned before its child reported ready: %v", err)
		default:
		}
		if time.Now().After(startDeadline) {
			t.Fatal("child process did not report ready before the deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("child process %d was not active after its start marker: %v", pid, err)
	}
	var err error
	select {
	case err = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("command did not return after its deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run error = %v, want context deadline", err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child process %d was not reaped after its deadline: %v", pid, err)
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
