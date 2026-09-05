package probe_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/probe"
	"github.com/azizu06/rehearse/internal/redact"
)

const commandSecretMarker = "issue-10-command-secret"
const boundarySecretMarker = "xYz987"

func TestTrustedCommandAndDataAssertionAreBoundedAndRedacted(t *testing.T) {
	t.Setenv("REHEARSE_SHOULD_NOT_BE_INHERITED", "ambient-secret")

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	dataRoot := t.TempDir()
	contents := []byte("restored-order-count=42\n")
	if err := os.WriteFile(dataRoot+"/orders.check", contents, 0o600); err != nil {
		t.Fatalf("write data assertion fixture: %v", err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(contents))

	configuration, err := probe.ParseConfig(strings.NewReader(fmt.Sprintf(`{
  "schema_version": "rehearse.probes/v1",
  "probes": [
    {
      "ordinal": 1,
      "id": "trusted-command",
      "kind": "command",
      "required": true,
      "retry": {"deadline": "2s", "backoff": "10ms", "max_attempts": 1},
      "command": {
        "executable": %q,
		"args": ["-test.run=TestProbeCommandHelper", "--", "probe-helper"],
        "expected_exit_code": 0,
        "trust_acknowledged": true
      }
    },
    {
      "ordinal": 2,
      "id": "restored-data",
      "kind": "data",
      "required": true,
      "retry": {"deadline": "1s", "backoff": "10ms", "max_attempts": 1},
      "data": {"path": "orders.check", "sha256": %q}
    }
  ]
}`, executable, digest)))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	result := probe.NewRunner(probe.Options{
		DataRoot: dataRoot,
		Redactor: redact.New(commandSecretMarker),
	}).Run(context.Background(), configuration)
	if !result.RequiredPassed {
		t.Fatalf("required probes failed: %#v", result.Probes)
	}
	commandEvidence := result.Probes[0]
	if !commandEvidence.TrustedHostCommand || !commandEvidence.StdoutTruncated || !commandEvidence.StderrTruncated {
		t.Fatalf("command evidence did not preserve trust/bounds: %#v", commandEvidence)
	}
	if strings.Contains(commandEvidence.Observed, commandSecretMarker) || !strings.Contains(commandEvidence.Observed, redact.Replacement) {
		t.Fatalf("command evidence was not redacted: %q", commandEvidence.Observed)
	}
	if got := len(commandEvidence.Observed); got > 2*(16<<10)+128 {
		t.Fatalf("combined command evidence length = %d, want bounded", got)
	}
	if result.Probes[1].Status != probe.StatusPassed {
		t.Fatalf("data assertion = %#v", result.Probes[1])
	}
}

func TestProbeCommandHelper(t *testing.T) {
	isHelper := false
	for _, argument := range os.Args {
		if argument == "probe-helper" {
			isHelper = true
			break
		}
	}
	if !isHelper {
		t.Skip("helper process only")
	}
	if os.Getenv("REHEARSE_SHOULD_NOT_BE_INHERITED") != "" {
		t.Fatal("command inherited ambient secret environment")
	}
	if _, err := fmt.Fprint(os.Stdout, commandSecretMarker+strings.Repeat("o", 20<<10)); err != nil {
		t.Fatalf("write stdout: %v", err)
	}
	if _, err := fmt.Fprint(os.Stderr, strings.Repeat("e", 20<<10)); err != nil {
		t.Fatalf("write stderr: %v", err)
	}
}

func TestCommandRedactsMarkerAcrossCaptureBoundary(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	configuration, err := probe.ParseConfig(strings.NewReader(fmt.Sprintf(`{
  "schema_version":"rehearse.probes/v1",
  "probes":[{
    "ordinal":1,"id":"boundary-redaction","kind":"command","required":true,
    "retry":{"deadline":"3s","backoff":"10ms","max_attempts":1},
    "command":{"executable":%q,"args":["-test.run=TestProbeCommandRedactionBoundaryHelper","--","boundary-redact-helper"],"expected_exit_code":0,"trust_acknowledged":true}
  }]
}`, executable)))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	result := probe.NewRunner(probe.Options{Redactor: redact.New(boundarySecretMarker)}).Run(context.Background(), configuration)
	if result.Probes[0].Status != probe.StatusPassed {
		t.Fatalf("command evidence = %#v", result.Probes[0])
	}
	if strings.Contains(result.Probes[0].Observed, boundarySecretMarker) || strings.Contains(result.Probes[0].Observed, "xY") {
		t.Fatalf("command evidence leaked boundary marker: %q", result.Probes[0].Observed)
	}
}

func TestProbeCommandRedactionBoundaryHelper(t *testing.T) {
	isHelper := false
	for _, argument := range os.Args {
		if argument == "boundary-redact-helper" {
			isHelper = true
			break
		}
	}
	if !isHelper {
		t.Skip("helper process only")
	}
	if _, err := fmt.Fprint(os.Stdout, strings.Repeat("o", (16<<10)-2)+boundarySecretMarker); err != nil {
		t.Fatalf("write stdout: %v", err)
	}
}

func TestCommandDeadlineBoundsDescendantPipe(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	pidPath := t.TempDir() + "/descendant.pid"
	configuration, err := probe.ParseConfig(strings.NewReader(fmt.Sprintf(`{
  "schema_version":"rehearse.probes/v1",
  "probes":[{
    "ordinal":1,"id":"deadline-command","kind":"command","required":true,
    "retry":{"deadline":"250ms","backoff":"10ms","max_attempts":1},
    "command":{"executable":%q,"args":["-test.run=TestProbeCommandDeadlineHelper","--","deadline-helper",%q],"expected_exit_code":0,"trust_acknowledged":true}
  }]
}`, executable, pidPath)))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	started := time.Now()
	result := probe.NewRunner(probe.Options{}).Run(context.Background(), configuration)
	if got := time.Since(started); got > 2*time.Second {
		t.Fatalf("command returned after %s, want bounded descendant-pipe shutdown", got)
	}
	if result.Probes[0].Status != probe.StatusTimedOut {
		t.Fatalf("command evidence = %#v", result.Probes[0])
	}
	contents, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read descendant PID: %v", err)
	}
	pid, err := strconv.Atoi(string(contents))
	if err != nil {
		t.Fatalf("parse descendant PID: %v", err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("command descendant %d still running after deadline", pid)
	}
}

func TestProbeCommandDeadlineHelper(t *testing.T) {
	index := -1
	for offset, argument := range os.Args {
		if argument == "deadline-helper" {
			index = offset
			break
		}
	}
	if index < 0 || index+1 >= len(os.Args) {
		t.Skip("helper process only")
	}
	child := exec.Command("/bin/sh", "-c", "sleep 1")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatalf("start descendant: %v", err)
	}
	if err := os.WriteFile(os.Args[index+1], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatalf("write descendant PID: %v", err)
	}
	time.Sleep(time.Second)
}
