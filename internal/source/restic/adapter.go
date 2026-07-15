// Package restic implements the source contract through restic's documented
// JSON command-line boundary.
package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/source"
	"golang.org/x/mod/semver"
)

const (
	minimumVersion        = "v0.18.0"
	defaultMaxStdoutBytes = 8 << 20
	defaultMaxStderrBytes = 1 << 20
	hardMaxStdoutBytes    = 64 << 20
	hardMaxStderrBytes    = 8 << 20
)

var semanticVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+].*)?$`)
var snapshotIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// RepositoryKind identifies the repository backends supported by Issue #7.
type RepositoryKind string

const (
	RepositoryLocal RepositoryKind = "local"
	RepositoryS3    RepositoryKind = "s3"
)

// Repository is typed runtime configuration and cannot carry credentials.
type Repository struct {
	Kind     RepositoryKind
	Location string
}

// S3Credentials names out-of-band references for an S3-compatible repository.
type S3Credentials struct {
	AccessKeyID     drill.CredentialReference
	SecretAccessKey drill.CredentialReference
	SessionToken    *drill.CredentialReference
}

// Config contains only process paths, non-secret repository metadata, and
// credential references.
type Config struct {
	Binary            string
	Repository        Repository
	Password          drill.CredentialReference
	S3Credentials     *S3Credentials
	CredentialTempDir string
	MaxStdoutBytes    int64
	MaxStderrBytes    int64
}

// Adapter is one configured restic source.
type Adapter struct {
	config               Config
	resolver             source.CredentialResolver
	verification         sync.RWMutex
	preflightGate        chan struct{}
	preflightCalls       uint64
	preflightBatchFailed bool
	preflightReady       bool
	afterGateRelease     func()
}

// New validates static adapter configuration without resolving credentials.
func New(config Config, resolver source.CredentialResolver) (*Adapter, error) {
	if resolver == nil {
		return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "a credential resolver is required"}
	}
	if config.Binary == "" {
		config.Binary = "restic"
	}
	binary, err := exec.LookPath(config.Binary)
	if err != nil {
		return nil, &source.Failure{Kind: source.FailureUnavailable, Operation: "configure", SafeHint: "restic is not installed or executable"}
	}
	config.Binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "restic executable path is invalid"}
	}
	if strings.TrimSpace(config.Repository.Location) == "" {
		return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "restic repository location is required"}
	}
	if err := validateRepository(config.Repository); err != nil {
		return nil, err
	}
	if err := config.Password.Validate(); err != nil {
		return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "restic password reference is required"}
	}
	if config.Repository.Kind == RepositoryS3 && config.S3Credentials == nil {
		return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "S3 credential references are required"}
	}
	if config.Repository.Kind == RepositoryLocal && config.S3Credentials != nil {
		return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "S3 credentials are not valid for a local repository"}
	}
	if config.S3Credentials != nil {
		if err := config.S3Credentials.AccessKeyID.Validate(); err != nil {
			return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "S3 access-key reference is invalid"}
		}
		if err := config.S3Credentials.SecretAccessKey.Validate(); err != nil {
			return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "S3 secret-key reference is invalid"}
		}
		if config.S3Credentials.SessionToken != nil {
			if err := config.S3Credentials.SessionToken.Validate(); err != nil {
				return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "S3 session-token reference is invalid"}
			}
		}
	}
	if config.CredentialTempDir != "" && (!filepath.IsAbs(config.CredentialTempDir) || filepath.Clean(config.CredentialTempDir) != config.CredentialTempDir) {
		return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "credential temp directory must be a clean absolute path"}
	}
	if config.MaxStdoutBytes == 0 {
		config.MaxStdoutBytes = defaultMaxStdoutBytes
	}
	if config.MaxStderrBytes == 0 {
		config.MaxStderrBytes = defaultMaxStderrBytes
	}
	if config.MaxStdoutBytes < 1 || config.MaxStderrBytes < 1 {
		return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "restic output limits must be positive"}
	}
	if config.MaxStdoutBytes > hardMaxStdoutBytes || config.MaxStderrBytes > hardMaxStderrBytes {
		return nil, &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "restic output limits exceed the supported maximum"}
	}
	return &Adapter{config: config, resolver: resolver, preflightGate: make(chan struct{}, 1)}, nil
}

func validateRepository(repository Repository) error {
	if strings.TrimSpace(repository.Location) != repository.Location || strings.ContainsAny(repository.Location, "\r\n\x00") {
		return &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "restic repository location is invalid"}
	}
	switch repository.Kind {
	case RepositoryLocal:
		if !filepath.IsAbs(repository.Location) || filepath.Clean(repository.Location) != repository.Location {
			return &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "local restic repository must be a clean absolute path"}
		}
	case RepositoryS3:
		if !strings.HasPrefix(repository.Location, "s3:") {
			return &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "S3 restic repository must use an s3:http or s3:https location"}
		}
		endpoint, err := url.Parse(strings.TrimPrefix(repository.Location, "s3:"))
		if err != nil || endpoint.Scheme != "http" && endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path == "" || endpoint.Path == "/" {
			return &source.Failure{Kind: source.FailureInvalidInput, Operation: "configure", SafeHint: "S3 restic repository location is invalid or contains embedded credentials"}
		}
	default:
		return &source.Failure{Kind: source.FailureUnsupported, Operation: "configure", SafeHint: "restic repository kind is not supported"}
	}
	return nil
}

// Capabilities preflights the executable and its structured-output contract.
func (adapter *Adapter) Capabilities(ctx context.Context) (source.Capabilities, error) {
	adapter.beginPreflight()
	verified := false
	acquired := false
	defer func() {
		if acquired {
			<-adapter.preflightGate
			if adapter.afterGateRelease != nil {
				adapter.afterGateRelease()
			}
		}
		adapter.completePreflight(verified)
	}()
	select {
	case adapter.preflightGate <- struct{}{}:
		acquired = true
	case <-ctx.Done():
		return source.Capabilities{}, contextFailure("preflight", ctx.Err())
	}
	stdout, _, err := adapter.run(ctx, "preflight", []string{"LANG=C", "LC_ALL=C"}, "version", "--json")
	if err != nil {
		return source.Capabilities{}, err
	}
	version, err := decodeVersionMessage(stdout)
	if err != nil {
		return source.Capabilities{}, &source.Failure{
			Kind:      source.FailureCorruptOutput,
			Operation: "preflight",
			SafeHint:  "restic returned invalid version JSON",
		}
	}
	if !supportedVersion(version) {
		return source.Capabilities{}, &source.Failure{
			Kind:      source.FailureUnsupported,
			Operation: "preflight",
			SafeHint:  "restic 0.18.0 or newer is required",
		}
	}
	verified = true
	return source.Capabilities{
		AdapterVersion:  "restic/" + version,
		Lists:           true,
		Acquires:        true,
		ReportsProgress: true,
	}, nil
}

func (adapter *Adapter) beginPreflight() {
	adapter.verification.Lock()
	defer adapter.verification.Unlock()
	if adapter.preflightCalls == 0 {
		adapter.preflightBatchFailed = false
	}
	adapter.preflightCalls++
	adapter.preflightReady = false
}

func (adapter *Adapter) completePreflight(ready bool) {
	adapter.verification.Lock()
	defer adapter.verification.Unlock()
	if !ready {
		adapter.preflightBatchFailed = true
	}
	adapter.preflightCalls--
	if adapter.preflightCalls == 0 {
		adapter.preflightReady = ready && !adapter.preflightBatchFailed
	}
}

func (adapter *Adapter) requirePreflight(operation string) error {
	adapter.verification.RLock()
	ready := adapter.preflightReady && adapter.preflightCalls == 0
	adapter.verification.RUnlock()
	if ready {
		return nil
	}
	return &source.Failure{Kind: source.FailureUnsupported, Operation: operation, SafeHint: "restic capability preflight is required"}
}

func supportedVersion(version string) bool {
	candidate := "v" + version
	return semanticVersionPattern.MatchString(version) && semver.IsValid(candidate) && semver.Compare(candidate, minimumVersion) >= 0
}

var (
	errOutputLimit              = errors.New("process output limit exceeded")
	errInvalidVersionMessage    = errors.New("invalid restic version message")
	errInvalidResticMessageType = errors.New("invalid restic message_type")
	errInvalidRestoreSummary    = errors.New("invalid restic restore summary")
	errInvalidStructuredExit    = errors.New("invalid structured restic exit")
	errInconsistentExitCode     = errors.New("inconsistent structured restic exit code")
	errMissingStructuredExit    = errors.New("missing structured restic exit")
)

type boundedBuffer struct {
	buffer  bytes.Buffer
	limit   int64
	over    bool
	onLimit func()
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	remaining := buffer.limit - int64(buffer.buffer.Len())
	if remaining <= 0 {
		buffer.markOver()
		return len(data), errOutputLimit
	}
	if int64(len(data)) > remaining {
		_, _ = buffer.buffer.Write(data[:remaining])
		buffer.markOver()
		return len(data), errOutputLimit
	}
	return buffer.buffer.Write(data)
}

func (buffer *boundedBuffer) markOver() {
	buffer.over = true
	if buffer.onLimit != nil {
		buffer.onLimit()
	}
}

func (buffer *boundedBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}

func (adapter *Adapter) run(ctx context.Context, operation string, environment []string, args ...string) ([]byte, []byte, error) {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stdout = boundedBuffer{limit: adapter.config.MaxStdoutBytes, onLimit: cancel}
	var stderr = boundedBuffer{limit: adapter.config.MaxStderrBytes, onLimit: cancel}
	command := exec.CommandContext(commandCtx, adapter.config.Binary, args...)
	command.Env = environment
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return nil, nil, contextFailure(operation, ctx.Err())
	}
	if stdout.over || stderr.over || errors.Is(err, errOutputLimit) {
		return nil, nil, &source.Failure{Kind: source.FailureOutputLimit, Operation: operation, SafeHint: "restic output exceeded the configured limit"}
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return nil, nil, failureForExit(operation, exitError.ExitCode(), stderr.Bytes())
		}
		return nil, nil, &source.Failure{Kind: source.FailureUnavailable, Operation: operation, ExitCode: -1, SafeHint: "restic could not be started"}
	}
	return append([]byte(nil), stdout.Bytes()...), append([]byte(nil), stderr.Bytes()...), nil
}

type snapshotMessage struct {
	ID       string          `json:"id"`
	Time     string          `json:"time"`
	Hostname string          `json:"hostname"`
	Paths    []string        `json:"paths"`
	Tags     []string        `json:"tags"`
	Summary  snapshotSummary `json:"summary"`
}

type snapshotSummary struct {
	Files uint64 `json:"total_files_processed"`
	Bytes uint64 `json:"total_bytes_processed"`
}

// ListRecoveryPoints maps restic snapshot JSON into the source contract.
func (adapter *Adapter) ListRecoveryPoints(ctx context.Context) ([]source.RecoveryPoint, error) {
	if err := adapter.requirePreflight("list"); err != nil {
		return nil, err
	}
	var stdout []byte
	err := adapter.withCredentialEnvironment(ctx, "list", func(environment []string) error {
		var runErr error
		stdout, _, runErr = adapter.run(ctx, "list", environment, "--json", "--no-lock", "--no-cache", "snapshots")
		return runErr
	})
	if err != nil {
		return nil, err
	}

	var snapshots *[]snapshotMessage
	if err := json.Unmarshal(stdout, &snapshots); err != nil || snapshots == nil {
		return nil, &source.Failure{Kind: source.FailureCorruptOutput, Operation: "list", SafeHint: "restic returned invalid snapshot JSON"}
	}
	points := make([]source.RecoveryPoint, 0, len(*snapshots))
	for _, snapshot := range *snapshots {
		createdAt, err := time.Parse(time.RFC3339Nano, snapshot.Time)
		if err != nil || !snapshotIDPattern.MatchString(snapshot.ID) {
			return nil, &source.Failure{Kind: source.FailureCorruptOutput, Operation: "list", SafeHint: "restic returned invalid snapshot metadata"}
		}
		points = append(points, source.RecoveryPoint{
			ID:        snapshot.ID,
			CreatedAt: createdAt.UTC(),
			Host:      snapshot.Hostname,
			Paths:     append([]string(nil), snapshot.Paths...),
			Tags:      append([]string(nil), snapshot.Tags...),
			Files:     snapshot.Summary.Files,
			Bytes:     snapshot.Summary.Bytes,
		})
	}
	return points, nil
}

func (adapter *Adapter) withCredentialEnvironment(
	ctx context.Context,
	operation string,
	use func([]string) error,
) (returnErr error) {
	parent := adapter.config.CredentialTempDir
	if parent == "" {
		parent = os.TempDir()
	}
	directory, err := os.MkdirTemp(parent, "rehearse-restic-credentials-")
	if err != nil {
		return &source.Failure{Kind: source.FailureUnavailable, Operation: operation, SafeHint: "temporary credential storage is unavailable"}
	}
	defer func() {
		if err := removePrivateTree(directory); err != nil {
			cleanupFailure := &source.Failure{Kind: source.FailureProcess, Operation: operation, SafeHint: "temporary credential files could not be removed"}
			if returnErr == nil {
				returnErr = cleanupFailure
			} else {
				returnErr = errors.Join(returnErr, cleanupFailure)
			}
		}
	}()
	if err := os.Chmod(directory, 0o700); err != nil {
		return &source.Failure{Kind: source.FailureUnavailable, Operation: operation, SafeHint: "temporary credential storage is unavailable"}
	}

	password, err := adapter.resolver.Resolve(ctx, adapter.config.Password)
	if err != nil {
		if ctx.Err() != nil {
			return contextFailure(operation, ctx.Err())
		}
		return &source.Failure{Kind: source.FailureAuthentication, Operation: operation, SafeHint: "restic repository credentials are unavailable"}
	}
	defer zero(password)
	passwordPath := filepath.Join(directory, "repository-password")
	if err := os.WriteFile(passwordPath, password, 0o600); err != nil {
		return &source.Failure{Kind: source.FailureUnavailable, Operation: operation, SafeHint: "temporary credential storage is unavailable"}
	}
	if err := os.Chmod(passwordPath, 0o600); err != nil {
		return &source.Failure{Kind: source.FailureUnavailable, Operation: operation, SafeHint: "temporary credential storage is unavailable"}
	}

	environment := []string{
		"LANG=C",
		"LC_ALL=C",
		"RESTIC_REPOSITORY=" + adapter.config.Repository.Location,
		"RESTIC_PASSWORD_FILE=" + passwordPath,
		"AWS_EC2_METADATA_DISABLED=true",
	}
	if adapter.config.S3Credentials != nil {
		credentialsPath, err := adapter.writeS3CredentialFile(ctx, operation, directory)
		if err != nil {
			return err
		}
		environment = append(environment,
			"AWS_SHARED_CREDENTIALS_FILE="+credentialsPath,
			"AWS_PROFILE=default",
		)
	}
	return use(environment)
}

func (adapter *Adapter) writeS3CredentialFile(ctx context.Context, operation, directory string) (string, error) {
	credentials := adapter.config.S3Credentials
	accessKey, err := adapter.resolveS3Credential(ctx, operation, credentials.AccessKeyID)
	if err != nil {
		return "", err
	}
	defer zero(accessKey)
	secretKey, err := adapter.resolveS3Credential(ctx, operation, credentials.SecretAccessKey)
	if err != nil {
		return "", err
	}
	defer zero(secretKey)
	var sessionToken []byte
	if credentials.SessionToken != nil {
		sessionToken, err = adapter.resolveS3Credential(ctx, operation, *credentials.SessionToken)
		if err != nil {
			return "", err
		}
		defer zero(sessionToken)
	}

	var contents bytes.Buffer
	contents.WriteString("[default]\naws_access_key_id = ")
	contents.Write(accessKey)
	contents.WriteString("\naws_secret_access_key = ")
	contents.Write(secretKey)
	if sessionToken != nil {
		contents.WriteString("\naws_session_token = ")
		contents.Write(sessionToken)
	}
	contents.WriteByte('\n')
	defer zero(contents.Bytes())
	path := filepath.Join(directory, "aws-credentials")
	if err := os.WriteFile(path, contents.Bytes(), 0o600); err != nil {
		return "", &source.Failure{Kind: source.FailureUnavailable, Operation: operation, SafeHint: "temporary credential storage is unavailable"}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", &source.Failure{Kind: source.FailureUnavailable, Operation: operation, SafeHint: "temporary credential storage is unavailable"}
	}
	return path, nil
}

func (adapter *Adapter) resolveS3Credential(ctx context.Context, operation string, reference drill.CredentialReference) ([]byte, error) {
	value, err := adapter.resolver.Resolve(ctx, reference)
	if err != nil {
		if ctx.Err() != nil {
			return nil, contextFailure(operation, ctx.Err())
		}
		return nil, &source.Failure{Kind: source.FailureAuthentication, Operation: operation, SafeHint: "S3 credential references are unavailable"}
	}
	if len(value) == 0 || bytes.ContainsAny(value, "\r\n\x00") {
		zero(value)
		return nil, &source.Failure{Kind: source.FailureAuthentication, Operation: operation, SafeHint: "S3 credential reference resolved to an invalid value"}
	}
	return value, nil
}

func contextFailure(operation string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &source.Failure{Kind: source.FailureTimeout, Operation: operation, SafeHint: "restic operation timed out"}
	}
	return &source.Failure{Kind: source.FailureCancelled, Operation: operation, SafeHint: "restic operation was cancelled"}
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func removePrivateTree(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = os.Chmod(path, 0o700)
	_ = filepath.WalkDir(path, func(current string, entry fs.DirEntry, _ error) error {
		if entry != nil && entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		mode := fs.FileMode(0o600)
		if entry != nil && entry.IsDir() {
			mode = 0o700
		}
		_ = os.Chmod(current, mode)
		return nil
	})
	return os.RemoveAll(path)
}

type jsonObjectMember struct {
	name  string
	value json.RawMessage
}

func decodeJSONObjectMembers(raw []byte) ([]jsonObjectMember, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	var members []jsonObjectMember
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		name, ok := token.(string)
		if !ok {
			return nil, false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		members = append(members, jsonObjectMember{name: name, value: value})
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, false
	}
	return members, true
}

func decodeJSONString(raw json.RawMessage) (string, bool) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func decodeVersionMessage(raw []byte) (string, error) {
	members, ok := decodeJSONObjectMembers(raw)
	if !ok {
		return "", errInvalidVersionMessage
	}
	var messageTypeRaw json.RawMessage
	var versionRaw json.RawMessage
	messageTypeCount := 0
	versionCount := 0
	alternateMessageType := false
	alternateVersion := false
	for _, member := range members {
		switch member.name {
		case "message_type":
			messageTypeCount++
			messageTypeRaw = member.value
		case "version":
			versionCount++
			versionRaw = member.value
		default:
			alternateMessageType = alternateMessageType || strings.EqualFold(member.name, "message_type")
			alternateVersion = alternateVersion || strings.EqualFold(member.name, "version")
		}
	}
	if messageTypeCount != 1 || versionCount != 1 || alternateMessageType || alternateVersion {
		return "", errInvalidVersionMessage
	}
	messageType, ok := decodeJSONString(messageTypeRaw)
	if !ok || messageType != "version" {
		return "", errInvalidVersionMessage
	}
	version, ok := decodeJSONString(versionRaw)
	if !ok || version == "" {
		return "", errInvalidVersionMessage
	}
	return version, nil
}

func decodeResticMessageType(raw json.RawMessage) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", errInvalidResticMessageType
	}
	encoded, ok := object["message_type"]
	if !ok || bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return "", errInvalidResticMessageType
	}
	var messageType string
	if err := json.Unmarshal(encoded, &messageType); err != nil {
		return "", errInvalidResticMessageType
	}
	return messageType, nil
}

type restoreStatus struct {
	PercentDone   restoreFraction `json:"percent_done"`
	FilesRestored restoreCounter  `json:"files_restored"`
	TotalFiles    restoreCounter  `json:"total_files"`
	BytesRestored restoreCounter  `json:"bytes_restored"`
	TotalBytes    restoreCounter  `json:"total_bytes"`
}

type restoreSummary struct {
	FilesRestored restoreCounter `json:"files_restored"`
	TotalFiles    restoreCounter `json:"total_files"`
	BytesRestored restoreCounter `json:"bytes_restored"`
	TotalBytes    restoreCounter `json:"total_bytes"`
}

// restoreCounter accepts an omitted field as restic's documented zero value,
// while rejecting an explicit null or any non-uint64 representation.
type restoreCounter uint64

type restoreFraction float64

func decodeRestoreSummary(raw json.RawMessage) (restoreSummary, error) {
	messageType, err := decodeResticMessageType(raw)
	if err != nil || messageType != "summary" {
		return restoreSummary{}, errInvalidRestoreSummary
	}
	var summary restoreSummary
	if err := json.Unmarshal(raw, &summary); err != nil || summary.FilesRestored > summary.TotalFiles || summary.BytesRestored > summary.TotalBytes {
		return restoreSummary{}, errInvalidRestoreSummary
	}
	return summary, nil
}

func (counter *restoreCounter) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("restore counter must not be null")
	}
	var value uint64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*counter = restoreCounter(value)
	return nil
}

func (fraction *restoreFraction) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("restore fraction must not be null")
	}
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*fraction = restoreFraction(value)
	return nil
}

// Acquire restores one full snapshot into a private staging child, then
// atomically promotes it after a valid completion summary.
func (adapter *Adapter) Acquire(ctx context.Context, request source.AcquireRequest) (_ source.Artifact, returnErr error) {
	if err := adapter.requirePreflight("acquire"); err != nil {
		return source.Artifact{}, err
	}
	if !snapshotIDPattern.MatchString(request.RecoveryPointID) {
		return source.Artifact{}, &source.Failure{Kind: source.FailureInvalidInput, Operation: "acquire", SafeHint: "a full restic snapshot ID is required"}
	}
	if !filepath.IsAbs(request.Workspace) || filepath.Clean(request.Workspace) != request.Workspace {
		return source.Artifact{}, &source.Failure{Kind: source.FailureInvalidInput, Operation: "acquire", SafeHint: "acquisition workspace must be a clean absolute path"}
	}
	entries, err := os.ReadDir(request.Workspace)
	if err != nil || len(entries) != 0 {
		return source.Artifact{}, &source.Failure{Kind: source.FailureInvalidInput, Operation: "acquire", SafeHint: "acquisition workspace must be an empty directory"}
	}

	staging, err := os.MkdirTemp(request.Workspace, ".rehearse-acquire-")
	if err != nil {
		return source.Artifact{}, &source.Failure{Kind: source.FailureUnavailable, Operation: "acquire", SafeHint: "acquisition staging directory is unavailable"}
	}
	promoted := false
	defer func() {
		if !promoted {
			if err := removePrivateTree(staging); err != nil {
				cleanupFailure := &source.Failure{Kind: source.FailureProcess, Operation: "acquire", SafeHint: "partial acquired data could not be removed"}
				if returnErr == nil {
					returnErr = cleanupFailure
				} else {
					returnErr = errors.Join(returnErr, cleanupFailure)
				}
			}
		}
	}()

	err = adapter.withCredentialEnvironment(ctx, "acquire", func(environment []string) error {
		return adapter.runRestore(
			ctx,
			environment,
			request.Report,
			"--json",
			"--no-lock",
			"--no-cache",
			"restore",
			request.RecoveryPointID,
			"--target",
			staging,
			"--verify",
		)
	})
	if err != nil {
		return source.Artifact{}, err
	}

	token := strings.TrimPrefix(filepath.Base(staging), ".rehearse-acquire-")
	artifactPath := filepath.Join(request.Workspace, "artifact-"+token)
	if err := os.Rename(staging, artifactPath); err != nil {
		return source.Artifact{}, &source.Failure{Kind: source.FailureProcess, Operation: "acquire", SafeHint: "acquired artifact could not be finalized"}
	}
	promoted = true
	return source.Artifact{
		Kind:            source.ArtifactDirectory,
		Path:            artifactPath,
		RecoveryPointID: request.RecoveryPointID,
	}, nil
}

func (adapter *Adapter) runRestore(
	ctx context.Context,
	environment []string,
	report source.ProgressReporter,
	args ...string,
) error {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var stderr = boundedBuffer{limit: adapter.config.MaxStderrBytes, onLimit: cancel}
	command := exec.CommandContext(commandCtx, adapter.config.Binary, args...)
	command.Env = environment
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		if ctx.Err() != nil {
			return contextFailure("acquire", ctx.Err())
		}
		return &source.Failure{Kind: source.FailureProcess, Operation: "acquire"}
	}
	if err := command.Start(); err != nil {
		if ctx.Err() != nil {
			return contextFailure("acquire", ctx.Err())
		}
		return &source.Failure{Kind: source.FailureUnavailable, Operation: "acquire", SafeHint: "restic could not be started"}
	}

	limited := &io.LimitedReader{R: stdout, N: adapter.config.MaxStdoutBytes + 1}
	decoder := json.NewDecoder(limited)
	summarySeen := false
	lastPercent := float64(0)
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return finishRestore(
				ctx,
				command,
				cancel,
				limited,
				&stderr,
				&source.Failure{Kind: source.FailureCorruptOutput, Operation: "acquire", SafeHint: "restic returned invalid restore JSON"},
			)
		}

		messageType, err := decodeResticMessageType(raw)
		if err != nil {
			return finishRestore(ctx, command, cancel, limited, &stderr, &source.Failure{Kind: source.FailureCorruptOutput, Operation: "acquire", SafeHint: "restic returned invalid restore JSON"})
		}
		if summarySeen && (messageType == "status" || messageType == "error" || messageType == "summary") {
			return finishRestore(ctx, command, cancel, limited, &stderr, &source.Failure{Kind: source.FailureCorruptOutput, Operation: "acquire", SafeHint: "restic returned invalid restore completion sequence"})
		}
		switch messageType {
		case "status":
			var status restoreStatus
			if err := json.Unmarshal(raw, &status); err != nil {
				return finishRestore(ctx, command, cancel, limited, &stderr, &source.Failure{Kind: source.FailureCorruptOutput, Operation: "acquire", SafeHint: "restic returned invalid restore progress"})
			}
			percentDone := float64(status.PercentDone)
			if percentDone < lastPercent || percentDone < 0 || percentDone > 1 || status.FilesRestored > status.TotalFiles || status.BytesRestored > status.TotalBytes {
				return finishRestore(ctx, command, cancel, limited, &stderr, &source.Failure{Kind: source.FailureCorruptOutput, Operation: "acquire", SafeHint: "restic returned invalid restore progress"})
			}
			lastPercent = percentDone
			if report != nil {
				report(source.Progress{
					PercentDone: percentDone,
					FilesDone:   uint64(status.FilesRestored),
					TotalFiles:  uint64(status.TotalFiles),
					BytesDone:   uint64(status.BytesRestored),
					TotalBytes:  uint64(status.TotalBytes),
				})
			}
		case "summary":
			summary, err := decodeRestoreSummary(raw)
			if err != nil {
				return finishRestore(ctx, command, cancel, limited, &stderr, &source.Failure{Kind: source.FailureCorruptOutput, Operation: "acquire", SafeHint: "restic returned invalid restore summary"})
			}
			summarySeen = true
			if report != nil && lastPercent < 1 {
				report(source.Progress{
					PercentDone: 1,
					FilesDone:   uint64(summary.FilesRestored),
					TotalFiles:  uint64(summary.TotalFiles),
					BytesDone:   uint64(summary.BytesRestored),
					TotalBytes:  uint64(summary.TotalBytes),
				})
			}
		default:
			// Additive message types are explicitly ignored.
		}
	}
	err = finishRestore(ctx, command, cancel, limited, &stderr, nil)
	if err != nil {
		return err
	}
	if !summarySeen {
		return &source.Failure{Kind: source.FailureCorruptOutput, Operation: "acquire", SafeHint: "restic restore did not report completion"}
	}
	return nil
}

func finishRestore(
	ctx context.Context,
	command *exec.Cmd,
	cancel context.CancelFunc,
	stdout *io.LimitedReader,
	stderr *boundedBuffer,
	parseFailure error,
) error {
	if parseFailure != nil || stdout.N == 0 {
		cancel()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return contextFailure("acquire", ctx.Err())
	}
	if stdout.N == 0 || stderr.over {
		return &source.Failure{Kind: source.FailureOutputLimit, Operation: "acquire", SafeHint: "restic output exceeded the configured limit"}
	}
	if parseFailure != nil {
		return parseFailure
	}
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			return failureForExit("acquire", exitError.ExitCode(), stderr.Bytes())
		}
		return &source.Failure{Kind: source.FailureUnavailable, Operation: "acquire", ExitCode: -1, SafeHint: "restic could not be started"}
	}
	return nil
}

func validateStructuredExit(stderr []byte, exitCode int) error {
	decoder := json.NewDecoder(bytes.NewReader(stderr))
	exitMessageSeen := false
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errInvalidStructuredExit
		}
		messageType, code, hasCode, err := decodeStructuredExitObject(raw)
		if err != nil {
			return errInvalidStructuredExit
		}
		if messageType == "exit_error" {
			if !hasCode {
				return errInvalidStructuredExit
			}
			value, err := code.Int64()
			if err != nil {
				return errInvalidStructuredExit
			}
			if value != int64(exitCode) {
				return errInconsistentExitCode
			}
			exitMessageSeen = true
		}
	}
	if !exitMessageSeen {
		return errMissingStructuredExit
	}
	return nil
}

func decodeStructuredExitObject(raw json.RawMessage) (string, json.Number, bool, error) {
	members, ok := decodeJSONObjectMembers(raw)
	if !ok {
		return "", "", false, errInvalidStructuredExit
	}
	var messageTypeRaw json.RawMessage
	var codeRaw json.RawMessage
	messageTypeCount := 0
	codeCount := 0
	alternateMessageType := false
	alternateCode := false
	for _, member := range members {
		switch member.name {
		case "message_type":
			messageTypeCount++
			messageTypeRaw = member.value
		case "code":
			codeCount++
			codeRaw = member.value
		default:
			alternateMessageType = alternateMessageType || strings.EqualFold(member.name, "message_type")
			alternateCode = alternateCode || strings.EqualFold(member.name, "code")
		}
	}
	if messageTypeCount != 1 || alternateMessageType {
		return "", "", false, errInvalidStructuredExit
	}
	messageType, ok := decodeJSONString(messageTypeRaw)
	if !ok {
		return "", "", false, errInvalidStructuredExit
	}
	if messageType != "exit_error" {
		return messageType, "", false, nil
	}
	if codeCount != 1 || alternateCode {
		return "", "", false, errInvalidStructuredExit
	}
	codeDecoder := json.NewDecoder(bytes.NewReader(codeRaw))
	codeDecoder.UseNumber()
	var value any
	if err := codeDecoder.Decode(&value); err != nil {
		return "", "", false, errInvalidStructuredExit
	}
	code, ok := value.(json.Number)
	if !ok || codeDecoder.Decode(&struct{}{}) != io.EOF {
		return "", "", false, errInvalidStructuredExit
	}
	return messageType, code, true, nil
}

func failureForExit(operation string, exitCode int, stderr []byte) error {
	if err := validateStructuredExit(stderr, exitCode); err != nil {
		safeHint := "restic returned invalid error JSON"
		if errors.Is(err, errInconsistentExitCode) {
			safeHint = "restic returned inconsistent error JSON"
		} else if errors.Is(err, errMissingStructuredExit) {
			safeHint = "restic did not return structured error JSON"
		}
		return &source.Failure{Kind: source.FailureCorruptOutput, Operation: operation, ExitCode: exitCode, SafeHint: safeHint}
	}

	switch exitCode {
	case 10:
		return &source.Failure{Kind: source.FailureRepositoryMissing, Operation: operation, ExitCode: exitCode, SafeHint: "restic repository does not exist or is unavailable"}
	case 12:
		return &source.Failure{Kind: source.FailureAuthentication, Operation: operation, ExitCode: exitCode, SafeHint: "restic repository credentials were rejected"}
	case 130:
		return &source.Failure{Kind: source.FailureProcess, Operation: operation, ExitCode: exitCode, SafeHint: "restic process was interrupted independently of the caller"}
	default:
		return &source.Failure{Kind: source.FailureProcess, Operation: operation, ExitCode: exitCode, SafeHint: "restic command failed"}
	}
}

var _ io.Writer = (*boundedBuffer)(nil)
var _ source.Adapter = (*Adapter)(nil)
