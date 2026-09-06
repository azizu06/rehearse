package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type dockerCommand interface {
	run(context.Context, int64, ...string) ([]byte, error)
}

type commandExecutor struct {
	binary      string
	environment []string
}

func newCommandExecutor(binary string) commandExecutor {
	if strings.TrimSpace(binary) == "" {
		binary = "docker"
	}
	return commandExecutor{binary: binary, environment: dockerEnvironment()}
}

func (executor commandExecutor) run(ctx context.Context, outputLimit int64, args ...string) ([]byte, error) {
	commandContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	output := &boundedBuffer{limit: outputLimit, cancel: cancel}
	command := exec.CommandContext(commandContext, executor.binary, args...)
	command.Env = executor.environment
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if cause := context.Cause(commandContext); cause != nil {
		return output.Bytes(), cause
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return output.Bytes(), fmt.Errorf("docker command failed with exit code %d", exitError.ExitCode())
		}
		return output.Bytes(), fmt.Errorf("start docker command: %w", err)
	}
	return output.Bytes(), nil
}

type boundedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int64
	cancel context.CancelCauseFunc
	once   sync.Once
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	remaining := buffer.limit - int64(buffer.buffer.Len())
	if remaining > 0 {
		writeLength := int64(len(data))
		if writeLength > remaining {
			writeLength = remaining
		}
		_, _ = buffer.buffer.Write(data[:writeLength])
	}
	exceeded := int64(len(data)) > remaining
	buffer.mu.Unlock()
	if exceeded {
		buffer.once.Do(func() { buffer.cancel(ErrOutputLimitExceeded) })
	}
	return len(data), nil
}

func (buffer *boundedBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return bytes.Clone(buffer.buffer.Bytes())
}

func dockerEnvironment() []string {
	allowed := []string{
		"DOCKER_CERT_PATH",
		"DOCKER_CONFIG",
		"DOCKER_CONTEXT",
		"DOCKER_HOST",
		"DOCKER_TLS_VERIFY",
		"HOME",
		"PATH",
		"XDG_RUNTIME_DIR",
	}
	environment := make([]string, 0, len(allowed)+3)
	for _, key := range allowed {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	return append(environment, "COMPOSE_ANSI=never", "COMPOSE_MENU=false", "COMPOSE_PROGRESS=plain")
}
