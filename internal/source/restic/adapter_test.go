package restic_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/source"
	resticadapter "github.com/azizu06/rehearse/internal/source/restic"
)

type credentialResolver func(context.Context, drill.CredentialReference) ([]byte, error)

func (resolve credentialResolver) Resolve(ctx context.Context, reference drill.CredentialReference) ([]byte, error) {
	return resolve(ctx, reference)
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	done     chan struct{}
	once     sync.Once
}

func (ctx *observedDoneContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.observed) })
	return ctx.done
}

func TestDefaultCredentialTempDirIsFrozenAtConstruction(t *testing.T) {
	initialTempDir := t.TempDir()
	repository := t.TempDir()
	t.Setenv("TMPDIR", initialTempDir)

	adapter := newTestAdapter(t, resticadapter.Config{
		Binary: writeFakeResticScript(t, `
case " $* " in
  *" version "*) printf '%s\n' '{"message_type":"version","version":"0.19.1"}' ;;
  *" snapshots "*)
    case "$RESTIC_PASSWORD_FILE" in
      `+shellQuote(initialTempDir)+`/*) printf '%s\n' '[]' ;;
      *) exit 91 ;;
    esac
    ;;
esac
`),
		Repository: resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: repository},
		Password:   drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
	})

	t.Setenv("TMPDIR", repository)
	if _, err := adapter.ListRecoveryPoints(context.Background()); err != nil {
		t.Fatalf("list with mutated TMPDIR: %v", err)
	}
	assertDirectoryEmpty(t, initialTempDir)
	assertDirectoryEmpty(t, repository)
}

func TestListMapsStructuredExitErrorsWithoutLeakingOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		exitCode int
		wantKind source.FailureKind
	}{
		{name: "missing repository", exitCode: 10, wantKind: source.FailureRepositoryMissing},
		{name: "wrong password", exitCode: 12, wantKind: source.FailureAuthentication},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			const privateOutput = "credential-marker-must-not-leak /private/restored/item"
			credentialTempDir := t.TempDir()
			binary := writeFakeResticScript(t, `
printf '%s\n' '{"message_type":"future_error","message":"`+privateOutput+`"}' >&2
printf '%s\n' '{"message_type":"exit_error","Message_Type":"future_error","code":`+strconv.Itoa(test.exitCode)+`,"Code":999,"message":"`+privateOutput+`"}' >&2
exit `+strconv.Itoa(test.exitCode)+`
`)
			adapter := newTestAdapter(t, resticadapter.Config{
				Binary: binary,
				Repository: resticadapter.Repository{
					Kind:     resticadapter.RepositoryLocal,
					Location: t.TempDir(),
				},
				Password: drill.CredentialReference{
					Provider: drill.CredentialEnvironment,
					Locator:  "REHEARSE_TEST_RESTIC_PASSWORD",
				},
				CredentialTempDir: credentialTempDir,
			})

			_, err := adapter.ListRecoveryPoints(context.Background())
			var failure *source.Failure
			if !errors.As(err, &failure) {
				t.Fatalf("error = %v, want typed source failure", err)
			}
			if failure.Kind != test.wantKind || failure.ExitCode != test.exitCode {
				t.Fatalf("failure = %+v, want kind %q and exit %d", failure, test.wantKind, test.exitCode)
			}
			if strings.Contains(err.Error(), privateOutput) || strings.Contains(err.Error(), "/private/restored/item") {
				t.Fatalf("private process output leaked through error: %v", err)
			}
			entries, readErr := os.ReadDir(credentialTempDir)
			if readErr != nil {
				t.Fatalf("read credential temp dir: %v", readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("credential files remain after exit %d: %v", test.exitCode, entries)
			}
		})
	}
}

func TestListTreatsIndependentExit130AsProcessFailure(t *testing.T) {
	t.Parallel()

	const privateOutput = "private-independent-interrupt-marker"
	credentialTempDir := t.TempDir()
	adapter := newTestAdapter(t, resticadapter.Config{
		Binary: writeFakeResticScript(t, `
printf '%s\n' '{"message_type":"exit_error","code":130,"message":"`+privateOutput+`"}' >&2
exit 130
`),
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: credentialTempDir,
	})

	_, err := adapter.ListRecoveryPoints(context.Background())
	var failure *source.Failure
	if !errors.As(err, &failure) || failure.Kind != source.FailureProcess || failure.ExitCode != 130 {
		t.Fatalf("failure = %+v, want process failure with exit 130", failure)
	}
	if strings.Contains(err.Error(), privateOutput) {
		t.Fatalf("private process output leaked through error: %v", err)
	}
	assertDirectoryEmpty(t, credentialTempDir)
}

func TestListRejectsInvalidErrorMessageType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
	}{
		{name: "missing", message: `{"future_field":true}`},
		{name: "null", message: `{"message_type":null}`},
		{name: "wrong type", message: `{"message_type":false}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			credentialTempDir := t.TempDir()
			adapter := newTestAdapter(t, resticadapter.Config{
				Binary: writeFakeResticScript(t, `
printf '%s\n' '`+test.message+`' >&2
printf '%s\n' '{"message_type":"exit_error","code":12}' >&2
exit 12
`),
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: credentialTempDir,
			})

			_, err := adapter.ListRecoveryPoints(context.Background())
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != source.FailureCorruptOutput {
				t.Fatalf("failure = %+v, want corrupt output", failure)
			}
			assertDirectoryEmpty(t, credentialTempDir)
		})
	}
}

func TestListRejectsInvalidStructuredExitFields(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		safeHint string
	}{
		{name: "alternate message type case", message: `{"Message_Type":"exit_error","code":12}`, safeHint: "restic returned invalid error JSON"},
		{name: "alternate code case", message: `{"message_type":"exit_error","Code":12}`, safeHint: "restic returned invalid error JSON"},
		{name: "null code", message: `{"message_type":"exit_error","code":null}`, safeHint: "restic returned invalid error JSON"},
		{name: "string code", message: `{"message_type":"exit_error","code":"12"}`, safeHint: "restic returned invalid error JSON"},
		{name: "missing code", message: `{"message_type":"exit_error"}`, safeHint: "restic returned invalid error JSON"},
		{name: "duplicate code", message: `{"message_type":"exit_error","code":12,"code":12}`, safeHint: "restic returned invalid error JSON"},
		{name: "conflicting code", message: `{"message_type":"exit_error","code":10,"code":12}`, safeHint: "restic returned invalid error JSON"},
		{name: "duplicate message type", message: `{"message_type":"exit_error","message_type":"exit_error","code":12}`, safeHint: "restic returned invalid error JSON"},
		{name: "mismatched code", message: `{"message_type":"exit_error","code":10}`, safeHint: "restic returned inconsistent error JSON"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentialTempDir := t.TempDir()
			adapter := newTestAdapter(t, resticadapter.Config{
				Binary: writeFakeResticScript(t, `
printf '%s\n' '`+test.message+`' >&2
exit 12
`),
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: credentialTempDir,
			})

			_, err := adapter.ListRecoveryPoints(context.Background())
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != source.FailureCorruptOutput || failure.SafeHint != test.safeHint {
				t.Fatalf("failure = %+v, want corrupt output with hint %q", failure, test.safeHint)
			}
			assertDirectoryEmpty(t, credentialTempDir)
		})
	}
}

func TestListRecoveryPointsUsesCredentialFilesAndMapsJSON(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	credentialTempDir := t.TempDir()
	binary := writeFakeResticScript(t, `
test "$RESTIC_REPOSITORY" = "`+repository+`" || exit 91
test -f "$RESTIC_PASSWORD_FILE" || exit 92
test "$(find "$RESTIC_PASSWORD_FILE" -perm 0600 -print)" = "$RESTIC_PASSWORD_FILE" || exit 93
	printf '%s\n' '[{"id":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","time":"2026-07-15T17:30:00Z","hostname":"db-1","paths":["/data"],"tags":["daily"],"summary":{"total_files_processed":2,"total_bytes_processed":128},"future_field":{"safe":"ignored"}}]'
`)
	adapter := newTestAdapter(t, resticadapter.Config{
		Binary: binary,
		Repository: resticadapter.Repository{
			Kind:     resticadapter.RepositoryLocal,
			Location: repository,
		},
		Password: drill.CredentialReference{
			Provider: drill.CredentialEnvironment,
			Locator:  "REHEARSE_TEST_RESTIC_PASSWORD",
		},
		CredentialTempDir: credentialTempDir,
	})

	points, err := adapter.ListRecoveryPoints(context.Background())
	if err != nil {
		t.Fatalf("list recovery points: %v", err)
	}
	want := []source.RecoveryPoint{{
		ID:        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		CreatedAt: time.Date(2026, 7, 15, 17, 30, 0, 0, time.UTC),
		Host:      "db-1",
		Paths:     []string{"/data"},
		Tags:      []string{"daily"},
		Files:     2,
		Bytes:     128,
	}}
	if !reflect.DeepEqual(points, want) {
		t.Fatalf("recovery points = %+v, want %+v", points, want)
	}
	entries, err := os.ReadDir(credentialTempDir)
	if err != nil {
		t.Fatalf("read credential temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("credential files remain after list: %v", entries)
	}
}

func TestListRejectsNullSnapshotArray(t *testing.T) {
	t.Parallel()

	credentialTempDir := t.TempDir()
	adapter := newTestAdapter(t, resticadapter.Config{
		Binary:            writeFakeRestic(t, "null"),
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: credentialTempDir,
	})

	_, err := adapter.ListRecoveryPoints(context.Background())
	var failure *source.Failure
	if !errors.As(err, &failure) || failure.Kind != source.FailureCorruptOutput {
		t.Fatalf("failure = %+v, want corrupt output", failure)
	}
	assertDirectoryEmpty(t, credentialTempDir)
}

func TestS3CredentialsAreMaterializedOnlyInPermissionRestrictedFiles(t *testing.T) {
	t.Parallel()

	credentialTempDir := t.TempDir()
	binary := writeFakeResticScript(t, `
if test "${1:-}" = "version"; then
  printf '%s\n' '{"message_type":"version","version":"0.19.1"}'
  exit 0
fi
test "$RESTIC_REPOSITORY" = "s3:http://minio.test/rehearse/repository" || exit 91
test -f "$AWS_SHARED_CREDENTIALS_FILE" || exit 92
test "$(find "$AWS_SHARED_CREDENTIALS_FILE" -perm 0600 -print)" = "$AWS_SHARED_CREDENTIALS_FILE" || exit 93
test -z "${AWS_ACCESS_KEY_ID:-}" || exit 94
test -z "${AWS_SECRET_ACCESS_KEY:-}" || exit 95
grep -q '^aws_access_key_id = fixture-access-key$' "$AWS_SHARED_CREDENTIALS_FILE" || exit 96
grep -q '^aws_secret_access_key = fixture-secret-key$' "$AWS_SHARED_CREDENTIALS_FILE" || exit 97
grep -q '^aws_session_token = fixture-session-token$' "$AWS_SHARED_CREDENTIALS_FILE" || exit 98
printf '%s\n' '[]'
chmod 0000 "$(dirname "$RESTIC_PASSWORD_FILE")"
`)
	references := map[string][]byte{
		"PASSWORD":      []byte("fixture-password"),
		"ACCESS_KEY":    []byte("fixture-access-key"),
		"SECRET_KEY":    []byte("fixture-secret-key"),
		"SESSION_TOKEN": []byte("fixture-session-token"),
	}
	sessionToken := drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "SESSION_TOKEN"}
	adapter, err := resticadapter.New(resticadapter.Config{
		Binary: binary,
		Repository: resticadapter.Repository{
			Kind:     resticadapter.RepositoryS3,
			Location: "s3:http://minio.test/rehearse/repository",
		},
		Password: drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "PASSWORD"},
		S3Credentials: &resticadapter.S3Credentials{
			AccessKeyID:     drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "ACCESS_KEY"},
			SecretAccessKey: drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "SECRET_KEY"},
			SessionToken:    &sessionToken,
		},
		CredentialTempDir: credentialTempDir,
	}, credentialResolver(func(_ context.Context, reference drill.CredentialReference) ([]byte, error) {
		value, ok := references[reference.Locator]
		if !ok {
			return nil, errors.New("unknown reference")
		}
		return append([]byte(nil), value...), nil
	}))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if _, err := adapter.Capabilities(context.Background()); err != nil {
		t.Fatalf("preflight adapter: %v", err)
	}

	points, err := adapter.ListRecoveryPoints(context.Background())
	if err != nil {
		t.Fatalf("list recovery points: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("unexpected recovery points: %+v", points)
	}
	assertDirectoryEmpty(t, credentialTempDir)
}

func TestNewSnapshotsCallerOwnedS3CredentialReferences(t *testing.T) {
	adapter, credentials, sessionToken, credentialTempDir := newS3SnapshotTestAdapter(t)
	const privateMutationMarker = "private-mutated-reference"
	credentials.AccessKeyID = drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: privateMutationMarker + "-access"}
	credentials.SecretAccessKey = drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: privateMutationMarker + "-secret"}
	*sessionToken = drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: privateMutationMarker + "-original-session"}
	replacement := drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: privateMutationMarker + "-replacement-session"}
	credentials.SessionToken = &replacement

	points, err := adapter.ListRecoveryPoints(context.Background())
	if err != nil {
		if strings.Contains(err.Error(), privateMutationMarker) || strings.Contains(err.Error(), "snapshot-secret-value") {
			t.Fatalf("caller mutation data leaked through error: %v", err)
		}
		t.Fatalf("list with snapshotted credentials: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("unexpected recovery points: %+v", points)
	}
	assertDirectoryEmpty(t, credentialTempDir)
}

func TestS3CredentialSnapshotDoesNotRaceCallerMutation(t *testing.T) {
	adapter, credentials, sessionToken, credentialTempDir := newS3SnapshotTestAdapter(t)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		firstSession := drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "MUTATED_SESSION_A"}
		secondSession := drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "MUTATED_SESSION_B"}
		for {
			select {
			case <-stop:
				return
			default:
				credentials.AccessKeyID = drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "MUTATED_ACCESS_A"}
				credentials.SecretAccessKey = drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "MUTATED_SECRET_A"}
				credentials.SessionToken = &firstSession
				*sessionToken = secondSession
				credentials.AccessKeyID = drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "MUTATED_ACCESS_B"}
				credentials.SecretAccessKey = drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "MUTATED_SECRET_B"}
				credentials.SessionToken = &secondSession
			}
		}
	}()

	var listErr error
	for range 8 {
		_, listErr = adapter.ListRecoveryPoints(context.Background())
		if listErr != nil {
			break
		}
	}
	close(stop)
	<-done
	if listErr != nil {
		if strings.Contains(listErr.Error(), "snapshot-secret-value") || strings.Contains(listErr.Error(), "MUTATED_") {
			t.Fatalf("credential data leaked through error: %v", listErr)
		}
		t.Fatalf("list during caller mutation: %v", listErr)
	}
	assertDirectoryEmpty(t, credentialTempDir)
}

func TestAcquireStagesSelectedSnapshotAndReportsAggregateProgress(t *testing.T) {
	t.Parallel()

	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	repository := t.TempDir()
	credentialTempDir := t.TempDir()
	binary := writeFakeResticScript(t, `
test "$RESTIC_REPOSITORY" = "`+repository+`" || exit 91
snapshot=""
target=""
while test "$#" -gt 0; do
  case "$1" in
    restore) snapshot="$2"; shift ;;
    --target) target="$2"; shift ;;
  esac
  shift
done
test "$snapshot" = "`+recoveryPointID+`" || exit 92
test -n "$target" || exit 93
mkdir -p "$target/data"
printf 'recovered' > "$target/data/orders.db"
printf '%s\n' '{"message_type":"future_progress","private_path":"must-be-ignored"}'
printf '%s\n' '{"message_type":"status","Message_Type":"future","percent_done":0.5,"files_restored":1,"Files_Restored":999,"total_files":2,"bytes_restored":64,"total_bytes":128,"future_field":true}'
printf '%s\n' '{"message_type":"summary","Message_Type":"future","files_restored":2,"Files_Restored":999,"total_files":2,"bytes_restored":128,"total_bytes":128}'
`)
	adapter := newTestAdapter(t, resticadapter.Config{
		Binary: binary,
		Repository: resticadapter.Repository{
			Kind:     resticadapter.RepositoryLocal,
			Location: repository,
		},
		Password: drill.CredentialReference{
			Provider: drill.CredentialEnvironment,
			Locator:  "REHEARSE_TEST_RESTIC_PASSWORD",
		},
		CredentialTempDir: credentialTempDir,
	})
	workspace := t.TempDir()
	var progress []source.Progress

	artifact, err := adapter.Acquire(context.Background(), source.AcquireRequest{
		RecoveryPointID: recoveryPointID,
		Workspace:       workspace,
		Report: func(event source.Progress) {
			progress = append(progress, event)
		},
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if artifact.Kind != source.ArtifactDirectory || artifact.RecoveryPointID != recoveryPointID {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
	if filepath.Dir(artifact.Path) != workspace || strings.HasPrefix(filepath.Base(artifact.Path), ".") {
		t.Fatalf("artifact was not promoted inside the workspace: %q", artifact.Path)
	}
	contents, err := os.ReadFile(filepath.Join(artifact.Path, "data", "orders.db"))
	if err != nil {
		t.Fatalf("read acquired artifact: %v", err)
	}
	if string(contents) != "recovered" {
		t.Fatalf("acquired contents = %q", contents)
	}
	wantProgress := []source.Progress{{
		PercentDone: 0.5,
		FilesDone:   1,
		TotalFiles:  2,
		BytesDone:   64,
		TotalBytes:  128,
	}, {
		PercentDone: 1,
		FilesDone:   2,
		TotalFiles:  2,
		BytesDone:   128,
		TotalBytes:  128,
	}}
	if !reflect.DeepEqual(progress, wantProgress) {
		t.Fatalf("progress = %+v, want %+v", progress, wantProgress)
	}
	entries, err := os.ReadDir(credentialTempDir)
	if err != nil {
		t.Fatalf("read credential temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("credential files remain after acquire: %v", entries)
	}
}

func TestAcquireRejectsInvalidOrMissingSummaryBeforePromotion(t *testing.T) {
	tests := []struct {
		name    string
		summary string
	}{
		{name: "completion marker missing", summary: `{"message_type":"future_summary"}`},
		{name: "files restored null", summary: `{"message_type":"summary","files_restored":null}`},
		{name: "total files null", summary: `{"message_type":"summary","total_files":null}`},
		{name: "bytes restored null", summary: `{"message_type":"summary","bytes_restored":null}`},
		{name: "total bytes null", summary: `{"message_type":"summary","total_bytes":null}`},
		{name: "files restored wrong type", summary: `{"message_type":"summary","files_restored":"0"}`},
		{name: "total files wrong type", summary: `{"message_type":"summary","total_files":false}`},
		{name: "bytes restored wrong type", summary: `{"message_type":"summary","bytes_restored":0.5}`},
		{name: "total bytes wrong type", summary: `{"message_type":"summary","total_bytes":[]}`},
		{name: "files exceed total", summary: `{"message_type":"summary","files_restored":1}`},
		{name: "bytes exceed total", summary: `{"message_type":"summary","bytes_restored":1}`},
		{name: "duplicate discriminator promotes summary", summary: `{"message_type":"future","message_type":"summary"}`},
		{name: "case-variant counter cannot hide invalid exact value", summary: `{"message_type":"summary","files_restored":2,"Files_Restored":0,"total_files":1}`},
		{name: "duplicate counter cannot hide invalid first value", summary: `{"message_type":"summary","files_restored":2,"files_restored":0,"total_files":1}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertCorruptAcquireOutput(t, "printf '%s\\n' "+shellQuote(test.summary), "")
		})
	}
}

func TestAcquireRejectsKnownMessagesAfterSummary(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		safeHint string
	}{
		{
			name: "duplicate summary",
			output: `printf '%s\n' '{"message_type":"summary"}'
printf '%s\n' '{"message_type":"summary"}'`,
			safeHint: "restic returned invalid restore completion sequence",
		},
		{
			name: "status after summary",
			output: `printf '%s\n' '{"message_type":"summary"}'
printf '%s\n' '{"message_type":"status"}'`,
			safeHint: "restic returned invalid restore completion sequence",
		},
		{
			name: "error after summary",
			output: `printf '%s\n' '{"message_type":"summary"}'
printf '%s\n' '{"message_type":"error"}'`,
			safeHint: "restic returned invalid restore completion sequence",
		},
		{
			name: "duplicate discriminator hides status after summary",
			output: `printf '%s\n' '{"message_type":"summary"}'
printf '%s\n' '{"message_type":"status","message_type":"future"}'`,
			safeHint: "restic returned invalid restore JSON",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertCorruptAcquireOutput(t, test.output, test.safeHint)
		})
	}
}

func TestAcquireRejectsInvalidStatusBeforePromotion(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "percent done null", status: `{"message_type":"status","percent_done":null}`},
		{name: "files restored null", status: `{"message_type":"status","files_restored":null}`},
		{name: "total files null", status: `{"message_type":"status","total_files":null}`},
		{name: "bytes restored null", status: `{"message_type":"status","bytes_restored":null}`},
		{name: "total bytes null", status: `{"message_type":"status","total_bytes":null}`},
		{name: "percent done wrong type", status: `{"message_type":"status","percent_done":"0"}`},
		{name: "files restored wrong type", status: `{"message_type":"status","files_restored":"0"}`},
		{name: "total files wrong type", status: `{"message_type":"status","total_files":false}`},
		{name: "bytes restored wrong type", status: `{"message_type":"status","bytes_restored":0.5}`},
		{name: "total bytes wrong type", status: `{"message_type":"status","total_bytes":[]}`},
		{name: "case-variant counter cannot hide invalid exact value", status: `{"message_type":"status","files_restored":2,"Files_Restored":0,"total_files":1}`},
		{name: "duplicate counter cannot hide invalid first value", status: `{"message_type":"status","files_restored":2,"files_restored":0,"total_files":1}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertCorruptAcquireOutput(t, "printf '%s\\n' "+shellQuote(test.status)+"\nprintf '%s\\n' '{\"message_type\":\"summary\"}'", "")
		})
	}
}

func TestAcquireRejectsInvalidMessageTypeBeforePromotion(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{name: "missing", message: `{"future_field":true}`},
		{name: "null", message: `{"message_type":null}`},
		{name: "boolean", message: `{"message_type":false}`},
		{name: "number", message: `{"message_type":1}`},
		{name: "object", message: `{"message_type":{}}`},
		{name: "array", message: `{"message_type":[]}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertCorruptAcquireOutput(t, "printf '%s\\n' "+shellQuote(test.message)+"\nprintf '%s\\n' '{\"message_type\":\"summary\"}'", "")
		})
	}
}

func assertCorruptAcquireOutput(t *testing.T, processOutput string, safeHint string) {
	t.Helper()

	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	workspace := t.TempDir()
	credentialTempDir := t.TempDir()
	adapter := newTestAdapter(t, resticadapter.Config{
		Binary: writeFakeResticScript(t, `
target=""
while test "$#" -gt 0; do
  if test "$1" = "--target"; then target="$2"; shift; fi
  shift
done
mkdir -p "$target/private"
printf 'partial-private-content' > "$target/private/item"
`+processOutput+`
`),
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: credentialTempDir,
	})

	_, err := adapter.Acquire(context.Background(), source.AcquireRequest{
		RecoveryPointID: recoveryPointID,
		Workspace:       workspace,
	})
	var failure *source.Failure
	if !errors.As(err, &failure) || failure.Kind != source.FailureCorruptOutput {
		t.Fatalf("failure = %+v, want corrupt output", failure)
	}
	if safeHint != "" && failure.SafeHint != safeHint {
		t.Fatalf("safe hint = %q, want %q", failure.SafeHint, safeHint)
	}
	assertDirectoryEmpty(t, workspace)
	assertDirectoryEmpty(t, credentialTempDir)
}

func TestAcquirePromotesEmptyRestoreWithOmittedZeroCounters(t *testing.T) {
	t.Parallel()

	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	credentialTempDir := t.TempDir()
	adapter := newTestAdapter(t, resticadapter.Config{
		Binary: writeFakeResticScript(t, `
printf '%s\n' '{"message_type":"summary"}'
printf '%s\n' '{"message_type":"future_after_summary","future_field":true}'
`),
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: credentialTempDir,
	})
	workspace := t.TempDir()

	artifact, err := adapter.Acquire(context.Background(), source.AcquireRequest{
		RecoveryPointID: recoveryPointID,
		Workspace:       workspace,
	})
	if err != nil {
		t.Fatalf("acquire empty restore: %v", err)
	}
	if artifact.Kind != source.ArtifactDirectory || artifact.RecoveryPointID != recoveryPointID {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
	entries, err := os.ReadDir(artifact.Path)
	if err != nil {
		t.Fatalf("read promoted artifact: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty restore artifact contains entries: %v", entries)
	}
	credentialEntries, err := os.ReadDir(credentialTempDir)
	if err != nil {
		t.Fatalf("read credential temp dir: %v", err)
	}
	if len(credentialEntries) != 0 {
		t.Fatalf("credential files remain after acquire: %v", credentialEntries)
	}
}

func TestAcquireContextFailureTakesPrecedenceOverBoundaryErrors(t *testing.T) {
	t.Parallel()

	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	tests := []struct {
		name         string
		body         string
		maxStdout    int64
		maxStderr    int64
		removeBinary bool
		preCancel    bool
	}{
		{
			name:         "process start",
			body:         "exit 0\n",
			maxStdout:    1024,
			maxStderr:    1024,
			removeBinary: true,
			preCancel:    true,
		},
		{
			name: "JSON decode",
			body: `
printf '%s\n' '{"message_type":"status","percent_done":0.1,"files_restored":1,"total_files":10,"bytes_restored":1,"total_bytes":10}'
printf '%s\n' '{not-json'
`,
			maxStdout: 1024,
			maxStderr: 1024,
		},
		{
			name: "stdout limit",
			body: `
printf '%s\n' '{"message_type":"status","percent_done":0.1,"files_restored":1,"total_files":10,"bytes_restored":1,"total_bytes":10}'
printf '%s' '{"message_type":"future","private":"` + strings.Repeat("x", 256) + `"}'
`,
			maxStdout: 160,
			maxStderr: 1024,
		},
		{
			name: "stderr limit",
			body: `
printf '%s' '` + strings.Repeat("private-stderr-marker", 16) + `' >&2
printf '%s\n' '{"message_type":"status","percent_done":0.1,"files_restored":1,"total_files":10,"bytes_restored":1,"total_bytes":10}'
`,
			maxStdout: 1024,
			maxStderr: 64,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			credentialTempDir := t.TempDir()
			binary := writeFakeResticScript(t, test.body)
			adapter := newTestAdapter(t, resticadapter.Config{
				Binary:            binary,
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: credentialTempDir,
				MaxStdoutBytes:    test.maxStdout,
				MaxStderrBytes:    test.maxStderr,
			})
			if test.removeBinary {
				if err := os.Remove(binary); err != nil {
					t.Fatalf("remove fake restic: %v", err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.preCancel {
				cancel()
			}

			_, err := adapter.Acquire(ctx, source.AcquireRequest{
				RecoveryPointID: recoveryPointID,
				Workspace:       workspace,
				Report: func(source.Progress) {
					cancel()
				},
			})
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != source.FailureCancelled {
				t.Fatalf("failure = %+v, want cancelled", failure)
			}
			assertDirectoryEmpty(t, workspace)
			assertDirectoryEmpty(t, credentialTempDir)
		})
	}
}

func TestAcquireCancellationAndTimeoutRemovePartialStateAndCredentials(t *testing.T) {
	t.Parallel()

	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	tests := []struct {
		name             string
		context          func(t *testing.T) (context.Context, context.CancelFunc)
		wantKind         source.FailureKind
		cancelOnProgress bool
	}{
		{
			name: "cancelled",
			context: func(*testing.T) (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			wantKind:         source.FailureCancelled,
			cancelOnProgress: true,
		},
		{
			name: "timed out",
			context: func(*testing.T) (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 100*time.Millisecond)
			},
			wantKind: source.FailureTimeout,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			credentialTempDir := t.TempDir()
			binary := writeFakeResticScript(t, `
target=""
while test "$#" -gt 0; do
  if test "$1" = "--target"; then target="$2"; shift; fi
  shift
done
mkdir -p "$target/private"
printf 'partial-private-content' > "$target/private/item"
printf '%s\n' '{"message_type":"status","percent_done":0.1,"files_restored":1,"total_files":10,"bytes_restored":1,"total_bytes":10}'
exec sleep 30
`)
			adapter := newTestAdapter(t, resticadapter.Config{
				Binary: binary,
				Repository: resticadapter.Repository{
					Kind:     resticadapter.RepositoryLocal,
					Location: t.TempDir(),
				},
				Password: drill.CredentialReference{
					Provider: drill.CredentialEnvironment,
					Locator:  "REHEARSE_TEST_RESTIC_PASSWORD",
				},
				CredentialTempDir: credentialTempDir,
			})
			ctx, cancel := test.context(t)
			defer cancel()

			_, err := adapter.Acquire(ctx, source.AcquireRequest{
				RecoveryPointID: recoveryPointID,
				Workspace:       workspace,
				Report: func(source.Progress) {
					if test.cancelOnProgress {
						cancel()
					}
				},
			})
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != test.wantKind {
				t.Fatalf("failure = %+v, want kind %q", failure, test.wantKind)
			}
			workspaceEntries, readErr := os.ReadDir(workspace)
			if readErr != nil {
				t.Fatalf("read workspace: %v", readErr)
			}
			if len(workspaceEntries) != 0 {
				t.Fatalf("partial artifact remains: %v", workspaceEntries)
			}
			credentialEntries, readErr := os.ReadDir(credentialTempDir)
			if readErr != nil {
				t.Fatalf("read credential temp dir: %v", readErr)
			}
			if len(credentialEntries) != 0 {
				t.Fatalf("credential files remain: %v", credentialEntries)
			}
		})
	}
}

func TestAcquireRemovesPartialStateAndCredentialsOnCorruptOutputAndStartFailure(t *testing.T) {
	t.Parallel()

	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	tests := []struct {
		name         string
		removeBinary bool
		wantKind     source.FailureKind
	}{
		{name: "corrupt JSON", wantKind: source.FailureCorruptOutput},
		{name: "process start failure", removeBinary: true, wantKind: source.FailureUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			credentialTempDir := t.TempDir()
			binary := writeFakeResticScript(t, `
if test "${1:-}" = "version"; then
  printf '%s\n' '{"message_type":"version","version":"0.19.1"}'
  exit 0
fi
target=""
while test "$#" -gt 0; do
  if test "$1" = "--target"; then target="$2"; shift; fi
  shift
done
mkdir -p "$target/private"
printf 'partial-private-content' > "$target/private/item"
chmod 0000 "$target/private" "$target"
printf '%s\n' '{not-json'
`)
			adapter, err := resticadapter.New(resticadapter.Config{
				Binary:            binary,
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: credentialTempDir,
			}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
				return []byte("fixture-password"), nil
			}))
			if err != nil {
				t.Fatalf("new adapter: %v", err)
			}
			if _, err := adapter.Capabilities(context.Background()); err != nil {
				t.Fatalf("preflight adapter: %v", err)
			}
			if test.removeBinary {
				if err := os.Remove(binary); err != nil {
					t.Fatalf("remove fake restic: %v", err)
				}
			}

			_, err = adapter.Acquire(context.Background(), source.AcquireRequest{
				RecoveryPointID: recoveryPointID,
				Workspace:       workspace,
			})
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != test.wantKind {
				t.Fatalf("failure = %+v, want kind %q", failure, test.wantKind)
			}
			assertDirectoryEmpty(t, workspace)
			assertDirectoryEmpty(t, credentialTempDir)
		})
	}
}

func TestAcquirePreservesPrimaryFailureWhenStagingCleanupFails(t *testing.T) {
	t.Parallel()

	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	workspace := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(workspace, 0o700) })
	adapter := newTestAdapter(t, resticadapter.Config{
		Binary: writeFakeResticScript(t, `
target=""
while test "$#" -gt 0; do
  if test "$1" = "--target"; then target="$2"; shift; fi
  shift
done
mkdir -p "$target/private"
printf 'partial-private-content' > "$target/private/item"
chmod 0500 "$(dirname "$target")"
printf '%s\n' '{not-json'
`),
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: t.TempDir(),
	})

	_, err := adapter.Acquire(context.Background(), source.AcquireRequest{
		RecoveryPointID: recoveryPointID,
		Workspace:       workspace,
	})
	if chmodErr := os.Chmod(workspace, 0o700); chmodErr != nil {
		t.Fatalf("restore workspace permissions: %v", chmodErr)
	}
	if !hasSourceFailure(err, source.FailureCorruptOutput, "restic returned invalid restore JSON") {
		t.Fatalf("error = %v, want preserved corrupt-output failure", err)
	}
	if !hasSourceFailure(err, source.FailureProcess, "partial acquired data could not be removed") {
		t.Fatalf("error = %v, want typed staging-cleanup failure", err)
	}
	if strings.Contains(err.Error(), "partial-private-content") || strings.Contains(err.Error(), workspace) {
		t.Fatalf("private staging data leaked through error: %v", err)
	}
}

func TestListPreservesPrimaryFailureWhenCredentialCleanupFails(t *testing.T) {
	t.Parallel()

	credentialTempDir := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(credentialTempDir, 0o700) })
	adapter := newTestAdapter(t, resticadapter.Config{
		Binary: writeFakeResticScript(t, `
chmod 0500 "$(dirname "$(dirname "$RESTIC_PASSWORD_FILE")")"
printf '%s\n' '{"message_type":"exit_error","code":12,"message":"private-credential-marker"}' >&2
exit 12
`),
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: credentialTempDir,
	})

	_, err := adapter.ListRecoveryPoints(context.Background())
	if chmodErr := os.Chmod(credentialTempDir, 0o700); chmodErr != nil {
		t.Fatalf("restore credential temp permissions: %v", chmodErr)
	}
	if !hasSourceFailure(err, source.FailureAuthentication, "restic repository credentials were rejected") {
		t.Fatalf("error = %v, want preserved authentication failure", err)
	}
	if !hasSourceFailure(err, source.FailureProcess, "temporary credential files could not be removed") {
		t.Fatalf("error = %v, want typed credential-cleanup failure", err)
	}
	if strings.Contains(err.Error(), "private-credential-marker") {
		t.Fatalf("private credential data leaked through error: %v", err)
	}
}

func TestListBoundsStdoutAndStderrIndependently(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		maxStdout int64
		maxStderr int64
	}{
		{
			name:      "stdout",
			body:      "printf '%s' '" + strings.Repeat("private-stdout-marker", 16) + "'\n",
			maxStdout: 64,
			maxStderr: 1024,
		},
		{
			name:      "stderr",
			body:      "printf '%s' '" + strings.Repeat("private-stderr-marker", 16) + "' >&2\nexit 1\n",
			maxStdout: 1024,
			maxStderr: 64,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			credentialTempDir := t.TempDir()
			adapter := newTestAdapter(t, resticadapter.Config{
				Binary:            writeFakeResticScript(t, test.body),
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: credentialTempDir,
				MaxStdoutBytes:    test.maxStdout,
				MaxStderrBytes:    test.maxStderr,
			})

			_, err := adapter.ListRecoveryPoints(context.Background())
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != source.FailureOutputLimit {
				t.Fatalf("failure = %+v, want output limit", failure)
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatalf("bounded process output leaked through error: %v", err)
			}
			assertDirectoryEmpty(t, credentialTempDir)
		})
	}
}

func TestListTerminatesChattyChildAtOutputBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "stdout", body: `trap '' PIPE
while :; do printf '%064d' 0 || :; done`},
		{name: "stderr", body: `trap '' PIPE
while :; do printf '%064d' 0 >&2 || :; done`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			credentialTempDir := t.TempDir()
			adapter := newTestAdapter(t, resticadapter.Config{
				Binary:            writeFakeResticScript(t, test.body),
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: credentialTempDir,
				MaxStdoutBytes:    128,
				MaxStderrBytes:    128,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			started := time.Now()
			_, err := adapter.ListRecoveryPoints(ctx)
			if elapsed := time.Since(started); elapsed >= time.Second {
				t.Fatalf("chatty child termination took %s, want under 1s", elapsed)
			}
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != source.FailureOutputLimit {
				t.Fatalf("failure = %+v, want output limit", failure)
			}
			if ctx.Err() != nil {
				t.Fatalf("chatty child reached caller deadline instead of bounded termination: %v", ctx.Err())
			}
			assertDirectoryEmpty(t, credentialTempDir)
		})
	}
}

func TestAcquireTerminatesChattyChildAtOutputBoundsAndCleansUp(t *testing.T) {
	t.Parallel()

	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	tests := []struct {
		name string
		body string
	}{
		{name: "stdout", body: `target=""
while test "$#" -gt 0; do
  if test "$1" = "--target"; then target="$2"; shift; fi
  shift
done
mkdir -p "$target/private"
printf 'partial-private-content' > "$target/private/item"
trap '' PIPE
while :; do printf '%s\n' '{"message_type":"future","padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}' || :; done`},
		{name: "stderr", body: `target=""
while test "$#" -gt 0; do
  if test "$1" = "--target"; then target="$2"; shift; fi
  shift
done
mkdir -p "$target/private"
printf 'partial-private-content' > "$target/private/item"
trap '' PIPE
while :; do printf '%064d' 0 >&2 || :; done`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			credentialTempDir := t.TempDir()
			adapter := newTestAdapter(t, resticadapter.Config{
				Binary:            writeFakeResticScript(t, test.body),
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: credentialTempDir,
				MaxStdoutBytes:    128,
				MaxStderrBytes:    128,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			started := time.Now()
			_, err := adapter.Acquire(ctx, source.AcquireRequest{
				RecoveryPointID: recoveryPointID,
				Workspace:       workspace,
			})
			if elapsed := time.Since(started); elapsed >= time.Second {
				t.Fatalf("chatty child termination took %s, want under 1s", elapsed)
			}
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != source.FailureOutputLimit {
				t.Fatalf("failure = %+v, want output limit", failure)
			}
			if ctx.Err() != nil {
				t.Fatalf("chatty child reached caller deadline instead of bounded termination: %v", ctx.Err())
			}
			assertDirectoryEmpty(t, workspace)
			assertDirectoryEmpty(t, credentialTempDir)
		})
	}
}

func TestAcquirePrefersStderrLimitOverTruncatedStdoutAndCleansUp(t *testing.T) {
	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	workspace := t.TempDir()
	credentialTempDir := t.TempDir()
	adapter := newTestAdapter(t, resticadapter.Config{
		Binary: writeFakeResticScript(t, `
target=""
while test "$#" -gt 0; do
  if test "$1" = "--target"; then target="$2"; shift; fi
  shift
done
printf 'partial-private-content' > "$target/item"
printf '%s' '{"message_type":"status"'
trap '' PIPE
printf '%0256d' 0 >&2 || :
exec sleep 30
`),
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: credentialTempDir,
		MaxStdoutBytes:    1024,
		MaxStderrBytes:    128,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	started := time.Now()
	_, err := adapter.Acquire(ctx, source.AcquireRequest{
		RecoveryPointID: recoveryPointID,
		Workspace:       workspace,
	})
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("stderr overflow termination took %s, want under 1s", elapsed)
	}
	var failure *source.Failure
	if !errors.As(err, &failure) || failure.Kind != source.FailureOutputLimit {
		t.Fatalf("failure = %+v, want output limit", failure)
	}
	if ctx.Err() != nil {
		t.Fatalf("stderr overflow reached caller deadline: %v", ctx.Err())
	}
	assertDirectoryEmpty(t, workspace)
	assertDirectoryEmpty(t, credentialTempDir)
}

func TestAcquireBoundsStdoutAndStderrIndependentlyAndRemovesStaging(t *testing.T) {
	t.Parallel()

	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	tests := []struct {
		name      string
		body      string
		maxStdout int64
		maxStderr int64
	}{
		{
			name:      "stdout",
			body:      "printf '%s' '{\"message_type\":\"future\",\"private\":\"" + strings.Repeat("x", 256) + "\"}'\n",
			maxStdout: 64,
			maxStderr: 1024,
		},
		{
			name:      "stderr",
			body:      "printf '%s' '" + strings.Repeat("private-stderr-marker", 16) + "' >&2\nexit 1\n",
			maxStdout: 1024,
			maxStderr: 64,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			credentialTempDir := t.TempDir()
			adapter := newTestAdapter(t, resticadapter.Config{
				Binary:            writeFakeResticScript(t, test.body),
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: credentialTempDir,
				MaxStdoutBytes:    test.maxStdout,
				MaxStderrBytes:    test.maxStderr,
			})

			_, err := adapter.Acquire(context.Background(), source.AcquireRequest{
				RecoveryPointID: recoveryPointID,
				Workspace:       workspace,
			})
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != source.FailureOutputLimit {
				t.Fatalf("failure = %+v, want output limit", failure)
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatalf("bounded process output leaked through error: %v", err)
			}
			assertDirectoryEmpty(t, workspace)
			assertDirectoryEmpty(t, credentialTempDir)
		})
	}
}

func TestNewRejectsUnsafeRepositoryAndCredentialTempLocations(t *testing.T) {
	t.Parallel()

	binary := writeFakeRestic(t, `{"message_type":"version","version":"0.19.1"}`)
	tests := []struct {
		name       string
		repository resticadapter.Repository
		tempDir    string
		private    string
	}{
		{
			name:       "relative local repository",
			repository: resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: "relative/private-repository"},
			private:    "private-repository",
		},
		{
			name:       "S3 userinfo",
			repository: resticadapter.Repository{Kind: resticadapter.RepositoryS3, Location: "s3:http://user:private-secret@minio.test/bucket"},
			private:    "private-secret",
		},
		{
			name:       "S3 query credentials",
			repository: resticadapter.Repository{Kind: resticadapter.RepositoryS3, Location: "s3:http://minio.test/bucket?X-Amz-Credential=private-secret"},
			private:    "private-secret",
		},
		{
			name:       "unsupported backend",
			repository: resticadapter.Repository{Kind: "sftp", Location: "sftp:private-host:/repo"},
			private:    "private-host",
		},
		{
			name:       "relative credential temp directory",
			repository: resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
			tempDir:    "relative/private-credentials",
			private:    "private-credentials",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := resticadapter.Config{
				Binary:            binary,
				Repository:        test.repository,
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "PASSWORD_REFERENCE"},
				CredentialTempDir: test.tempDir,
			}
			if test.repository.Kind == resticadapter.RepositoryS3 {
				config.S3Credentials = &resticadapter.S3Credentials{
					AccessKeyID:     drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "ACCESS_REFERENCE"},
					SecretAccessKey: drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "SECRET_REFERENCE"},
				}
			}
			_, err := resticadapter.New(config, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
				return []byte("unused"), nil
			}))
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != source.FailureInvalidInput && failure.Kind != source.FailureUnsupported {
				t.Fatalf("failure = %+v, want invalid/unsupported typed failure", failure)
			}
			if strings.Contains(err.Error(), test.private) {
				t.Fatalf("unsafe location leaked through error: %v", err)
			}
		})
	}
}

func TestNewRejectsCredentialStorageInsideLocalRepository(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	nested := filepath.Join(repository, "credentials")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatalf("create nested credential directory: %v", err)
	}
	symlink := filepath.Join(t.TempDir(), "credential-link")
	if err := os.Symlink(nested, symlink); err != nil {
		t.Fatalf("create credential directory symlink: %v", err)
	}

	for _, credentialTempDir := range []string{repository, nested, symlink} {
		credentialTempDir := credentialTempDir
		t.Run(filepath.Base(credentialTempDir), func(t *testing.T) {
			t.Parallel()

			_, err := resticadapter.New(resticadapter.Config{
				Binary:            writeFakeRestic(t, `{"message_type":"version","version":"0.19.1"}`),
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: repository},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: credentialTempDir,
			}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
				return []byte("fixture-password"), nil
			}))
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != source.FailureInvalidInput || failure.SafeHint != "credential temp directory must be outside the local restic repository" {
				t.Fatalf("failure = %+v, want repository-overlap rejection", failure)
			}
		})
	}
}

func TestNewRejectsOutputLimitsAboveHardMaximum(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		maxStdout int64
		maxStderr int64
	}{
		{name: "stdout MaxInt64", maxStdout: math.MaxInt64, maxStderr: 1},
		{name: "stderr MaxInt64", maxStdout: 1, maxStderr: math.MaxInt64},
		{name: "stdout above hard maximum", maxStdout: 64<<20 + 1, maxStderr: 1},
		{name: "stderr above hard maximum", maxStdout: 1, maxStderr: 8<<20 + 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := resticadapter.New(resticadapter.Config{
				Binary:            writeFakeRestic(t, `{"message_type":"version","version":"0.19.1"}`),
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: t.TempDir(),
				MaxStdoutBytes:    test.maxStdout,
				MaxStderrBytes:    test.maxStderr,
			}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
				return []byte("fixture-password"), nil
			}))
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != source.FailureInvalidInput {
				t.Fatalf("failure = %+v, want invalid input", failure)
			}
		})
	}
}

func TestNewRejectsInvalidCredentialReferencesWithoutEchoingLocators(t *testing.T) {
	t.Parallel()

	const privateLocator = "inline-private-secret"
	_, err := resticadapter.New(resticadapter.Config{
		Binary:     writeFakeRestic(t, `{"message_type":"version","version":"0.19.1"}`),
		Repository: resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:   drill.CredentialReference{Provider: "inline", Locator: privateLocator},
	}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
		return []byte("unused"), nil
	}))
	var failure *source.Failure
	if !errors.As(err, &failure) || failure.Kind != source.FailureInvalidInput {
		t.Fatalf("failure = %+v, want invalid input", failure)
	}
	if strings.Contains(err.Error(), privateLocator) {
		t.Fatalf("credential locator leaked through error: %v", err)
	}
}

func TestCapabilitiesRequiresStructuredResticVersionAtLeast018(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      string
		wantVersion string
		wantErr     string
	}{
		{
			name:        "supported",
			output:      `{"message_type":"version","version":"0.19.1","future_field":"ignored"}`,
			wantVersion: "restic/0.19.1",
		},
		{
			name:        "stable minimum",
			output:      `{"message_type":"version","version":"0.18.0"}`,
			wantVersion: "restic/0.18.0",
		},
		{
			name:        "newer prerelease",
			output:      `{"message_type":"version","version":"0.18.1-rc.1"}`,
			wantVersion: "restic/0.18.1-rc.1",
		},
		{
			name:        "next major",
			output:      `{"message_type":"version","version":"1.0.0"}`,
			wantVersion: "restic/1.0.0",
		},
		{
			name:        "minimum with build metadata",
			output:      `{"message_type":"version","version":"0.18.0+build.1"}`,
			wantVersion: "restic/0.18.0+build.1",
		},
		{
			name:    "too old",
			output:  `{"message_type":"version","version":"0.17.3"}`,
			wantErr: "restic 0.18.0 or newer is required",
		},
		{
			name:    "incomplete minimum",
			output:  `{"message_type":"version","version":"0.18"}`,
			wantErr: "restic 0.18.0 or newer is required",
		},
		{
			name:    "prerelease below stable minimum",
			output:  `{"message_type":"version","version":"0.18.0-rc.1"}`,
			wantErr: "restic 0.18.0 or newer is required",
		},
		{
			name:    "invalid semantic version",
			output:  `{"message_type":"version","version":"0.18.00"}`,
			wantErr: "restic 0.18.0 or newer is required",
		},
		{
			name:    "plain text is not a capability contract",
			output:  "restic 0.19.1 compiled with go1.26.4 on darwin/arm64",
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "alternate message type case",
			output:  `{"Message_Type":"version","version":"0.19.1"}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "alternate version case",
			output:  `{"message_type":"version","Version":"0.19.1"}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:        "case variants alongside exact keys",
			output:      `{"message_type":"version","Message_Type":"version","version":"0.19.1","Version":"0.19.1"}`,
			wantVersion: "restic/0.19.1",
		},
		{
			name:    "missing message type",
			output:  `{"version":"0.19.1"}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "missing version",
			output:  `{"message_type":"version"}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "null message type",
			output:  `{"message_type":null,"version":"0.19.1"}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "null version",
			output:  `{"message_type":"version","version":null}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "wrong message type representation",
			output:  `{"message_type":true,"version":"0.19.1"}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "wrong version representation",
			output:  `{"message_type":"version","version":19.1}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "duplicate message type",
			output:  `{"message_type":"version","message_type":"version","version":"0.19.1"}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "conflicting message type",
			output:  `{"message_type":"future","message_type":"version","version":"0.19.1"}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "duplicate version",
			output:  `{"message_type":"version","version":"0.19.1","version":"0.19.1"}`,
			wantErr: "restic returned invalid version JSON",
		},
		{
			name:    "conflicting version",
			output:  `{"message_type":"version","version":"0.17.0","version":"0.19.1"}`,
			wantErr: "restic returned invalid version JSON",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			binary := writeFakeRestic(t, test.output)
			adapter, err := resticadapter.New(resticadapter.Config{
				Binary: binary,
				Repository: resticadapter.Repository{
					Kind:     resticadapter.RepositoryLocal,
					Location: t.TempDir(),
				},
				Password: drill.CredentialReference{
					Provider: drill.CredentialEnvironment,
					Locator:  "REHEARSE_TEST_RESTIC_PASSWORD",
				},
				CredentialTempDir: t.TempDir(),
			}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
				return []byte("fixture-password"), nil
			}))
			if err != nil {
				t.Fatalf("new adapter: %v", err)
			}

			capabilities, err := adapter.Capabilities(context.Background())
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("capabilities error = %v, want containing %q", err, test.wantErr)
				}
				_, listErr := adapter.ListRecoveryPoints(context.Background())
				assertPreflightRequired(t, listErr, "list")
				return
			}
			if err != nil {
				t.Fatalf("capabilities: %v", err)
			}
			if capabilities != (source.Capabilities{
				AdapterVersion:  test.wantVersion,
				Lists:           true,
				Acquires:        true,
				ReportsProgress: true,
			}) {
				t.Fatalf("unexpected capabilities: %+v", capabilities)
			}
		})
	}
}

func TestAdapterRequiresSuccessfulPreflightBeforeRepositoryOperations(t *testing.T) {
	t.Parallel()

	const recoveryPointID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	workspace := t.TempDir()
	credentialTempDir := t.TempDir()
	resolveCalls := 0
	adapter, err := resticadapter.New(resticadapter.Config{
		Binary: writeFakeResticScript(t, `
case " $* " in
  *" version "*) printf '%s\n' '{"message_type":"version","version":"0.19.1"}' ;;
  *" snapshots "*) printf '%s\n' '[]' ;;
  *" restore "*)
    target=""
    while test "$#" -gt 0; do
      if test "$1" = "--target"; then target="$2"; shift; fi
      shift
    done
    printf 'restored' > "$target/item"
    printf '%s\n' '{"message_type":"summary"}'
    ;;
  *) exit 90 ;;
esac
`),
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: credentialTempDir,
	}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
		resolveCalls++
		return []byte("fixture-password"), nil
	}))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	_, listErr := adapter.ListRecoveryPoints(context.Background())
	assertPreflightRequired(t, listErr, "list")
	_, acquireErr := adapter.Acquire(context.Background(), source.AcquireRequest{
		RecoveryPointID: recoveryPointID,
		Workspace:       workspace,
	})
	assertPreflightRequired(t, acquireErr, "acquire")
	if resolveCalls != 0 {
		t.Fatalf("credential resolver called %d times before preflight, want 0", resolveCalls)
	}
	assertDirectoryEmpty(t, workspace)
	assertDirectoryEmpty(t, credentialTempDir)

	if _, err := adapter.Capabilities(context.Background()); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	points, err := adapter.ListRecoveryPoints(context.Background())
	if err != nil || len(points) != 0 {
		t.Fatalf("list after preflight = %+v, %v; want empty success", points, err)
	}
	artifact, err := adapter.Acquire(context.Background(), source.AcquireRequest{
		RecoveryPointID: recoveryPointID,
		Workspace:       workspace,
	})
	if err != nil {
		t.Fatalf("acquire after preflight: %v", err)
	}
	if artifact.RecoveryPointID != recoveryPointID {
		t.Fatalf("artifact = %+v, want recovery point %s", artifact, recoveryPointID)
	}
	assertDirectoryEmpty(t, credentialTempDir)
}

func TestFailedOrCancelledPreflightLeavesAdapterUnverified(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{
			name: "failed",
			body: `printf '%s\n' '{"message_type":"version","version":"0.19.1"}'`,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			},
		},
		{
			name: "cancelled",
			body: `sleep 10`,
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			failMarker := filepath.Join(t.TempDir(), "fail-preflight")
			binary := writeFakeResticScript(t, `
if test "$1" = "version"; then
  if test -e '`+failMarker+`'; then
    `+test.body+`
    exit 91
  fi
  printf '%s\n' '{"message_type":"version","version":"0.19.1"}'
  exit 0
fi
printf '%s\n' '[]'
`)
			adapter, err := resticadapter.New(resticadapter.Config{
				Binary:            binary,
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
				CredentialTempDir: t.TempDir(),
			}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
				return []byte("fixture-password"), nil
			}))
			if err != nil {
				t.Fatalf("new adapter: %v", err)
			}
			if _, err := adapter.Capabilities(context.Background()); err != nil {
				t.Fatalf("initial preflight: %v", err)
			}
			if err := os.WriteFile(failMarker, []byte("fail"), 0o600); err != nil {
				t.Fatalf("enable failing preflight: %v", err)
			}
			ctx, cancel := test.ctx()
			defer cancel()
			if _, err := adapter.Capabilities(ctx); err == nil {
				t.Fatal("failing preflight succeeded")
			}
			_, err = adapter.ListRecoveryPoints(context.Background())
			assertPreflightRequired(t, err, "list")
		})
	}
}

func TestOperationsCannotObserveInProgressPreflightAsVerified(t *testing.T) {
	t.Parallel()

	started := filepath.Join(t.TempDir(), "preflight-started")
	release := filepath.Join(t.TempDir(), "preflight-release")
	binary := writeFakeResticScript(t, `
if test "$1" = "version"; then
  if test -e '`+started+`'; then
    while ! test -e '`+release+`'; do sleep 0.01; done
  else
    : > '`+started+`'
  fi
  printf '%s\n' '{"message_type":"version","version":"0.19.1"}'
  exit 0
fi
printf '%s\n' '[]'
`)
	adapter, err := resticadapter.New(resticadapter.Config{
		Binary:            binary,
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: t.TempDir(),
	}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
		return []byte("fixture-password"), nil
	}))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if _, err := adapter.Capabilities(context.Background()); err != nil {
		t.Fatalf("initial preflight: %v", err)
	}

	preflightDone := make(chan error, 1)
	go func() {
		_, err := adapter.Capabilities(context.Background())
		preflightDone <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := adapter.ListRecoveryPoints(context.Background())
		if err != nil {
			assertPreflightRequired(t, err, "list")
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("in-progress preflight never revoked previous verification")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-preflightDone:
		t.Fatalf("preflight returned before release: %v", err)
	default:
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatalf("release preflight: %v", err)
	}
	if err := <-preflightDone; err != nil {
		t.Fatalf("complete preflight: %v", err)
	}
	if _, err := adapter.ListRecoveryPoints(context.Background()); err != nil {
		t.Fatalf("list after completed preflight: %v", err)
	}
}

func TestOverlappingPreflightFailureKeepsSuccessfulWaiterUnverified(t *testing.T) {
	started := filepath.Join(t.TempDir(), "preflight-started")
	release := filepath.Join(t.TempDir(), "preflight-release")
	binary := writeFakeResticScript(t, `
if test "$1" = "version"; then
  if ! test -e '`+started+`'; then
    : > '`+started+`'
    while ! test -e '`+release+`'; do sleep 0.01; done
    exit 91
  fi
  printf '%s\n' '{"message_type":"version","version":"0.19.1"}'
  exit 0
fi
printf '%s\n' '[]'
`)
	adapter, err := resticadapter.New(resticadapter.Config{
		Binary:            binary,
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: t.TempDir(),
	}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
		return []byte("fixture-password"), nil
	}))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	olderDone := make(chan error, 1)
	go func() {
		_, err := adapter.Capabilities(context.Background())
		olderDone <- err
	}()
	waitForPath(t, started)
	newerContext := &observedDoneContext{
		Context:  context.Background(),
		observed: make(chan struct{}),
		done:     make(chan struct{}),
	}
	newerDone := make(chan error, 1)
	go func() {
		_, err := adapter.Capabilities(newerContext)
		newerDone <- err
	}()
	<-newerContext.observed
	select {
	case err := <-newerDone:
		t.Fatalf("newer preflight completed before older release: %v", err)
	default:
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatalf("release older preflight: %v", err)
	}
	if err := <-olderDone; err == nil {
		t.Fatal("older preflight succeeded")
	}
	if err := <-newerDone; err != nil {
		t.Fatalf("newer preflight: %v", err)
	}
	_, err = adapter.ListRecoveryPoints(context.Background())
	assertPreflightRequired(t, err, "list")
	if _, err := adapter.Capabilities(context.Background()); err != nil {
		t.Fatalf("recovery preflight: %v", err)
	}
	if _, err := adapter.ListRecoveryPoints(context.Background()); err != nil {
		t.Fatalf("list after recovery preflight: %v", err)
	}
}

func TestCancelledWaitingPreflightInvalidatesOverlappingSuccess(t *testing.T) {
	started := filepath.Join(t.TempDir(), "preflight-started")
	release := filepath.Join(t.TempDir(), "preflight-release")
	binary := writeFakeResticScript(t, `
if test "$1" = "version"; then
  if ! test -e '`+release+`'; then
    : > '`+started+`'
    while ! test -e '`+release+`'; do sleep 0.01; done
  fi
  printf '%s\n' '{"message_type":"version","version":"0.19.1"}'
  exit 0
fi
printf '%s\n' '[]'
`)
	adapter, err := resticadapter.New(resticadapter.Config{
		Binary:            binary,
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: t.TempDir(),
	}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
		return []byte("fixture-password"), nil
	}))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	activeDone := make(chan error, 1)
	go func() {
		_, err := adapter.Capabilities(context.Background())
		activeDone <- err
	}()
	waitForPath(t, started)
	waitingCtx, cancelWaiting := context.WithCancel(context.Background())
	cancelWaiting()
	startedWaiting := time.Now()
	_, err = adapter.Capabilities(waitingCtx)
	if elapsed := time.Since(startedWaiting); elapsed >= time.Second {
		t.Fatalf("cancelled waiting preflight took %s, want under 1s", elapsed)
	}
	var failure *source.Failure
	if !errors.As(err, &failure) || failure.Kind != source.FailureCancelled {
		t.Fatalf("waiting failure = %+v, want cancelled", failure)
	}
	_, err = adapter.ListRecoveryPoints(context.Background())
	assertPreflightRequired(t, err, "list")
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatalf("release active preflight: %v", err)
	}
	if err := <-activeDone; err != nil {
		t.Fatalf("active preflight: %v", err)
	}
	_, err = adapter.ListRecoveryPoints(context.Background())
	assertPreflightRequired(t, err, "list")
	if _, err := adapter.Capabilities(context.Background()); err != nil {
		t.Fatalf("recovery preflight: %v", err)
	}
	if _, err := adapter.ListRecoveryPoints(context.Background()); err != nil {
		t.Fatalf("list after recovery preflight: %v", err)
	}
}

func TestCapabilitiesDoesNotInheritParentEnvironment(t *testing.T) {
	const sentinel = "must-not-reach-restic-version"
	t.Setenv("REHEARSE_PREFLIGHT_SENTINEL", sentinel)
	t.Setenv("REHEARSE_TEST_RESTIC_PASSWORD", sentinel)
	t.Setenv("AWS_ACCESS_KEY_ID", sentinel)
	t.Setenv("AWS_SECRET_ACCESS_KEY", sentinel)

	adapter, err := resticadapter.New(resticadapter.Config{
		Binary: writeFakeResticScript(t, `
test -z "${REHEARSE_PREFLIGHT_SENTINEL:-}" || exit 91
test -z "${REHEARSE_TEST_RESTIC_PASSWORD:-}" || exit 92
test -z "${AWS_ACCESS_KEY_ID:-}" || exit 93
test -z "${AWS_SECRET_ACCESS_KEY:-}" || exit 94
printf '%s\n' '{"message_type":"version","version":"0.19.1"}'
`),
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: t.TempDir()},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "REHEARSE_TEST_RESTIC_PASSWORD"},
		CredentialTempDir: t.TempDir(),
	}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
		return []byte("fixture-password"), nil
	}))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	capabilities, err := adapter.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if capabilities.AdapterVersion != "restic/0.19.1" {
		t.Fatalf("adapter version = %q, want restic/0.19.1", capabilities.AdapterVersion)
	}
}

func writeFakeRestic(t *testing.T, output string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "restic")
	script := "#!/bin/sh\nprintf '%s\\n' '" + strings.ReplaceAll(output, "'", "'\\''") + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake restic: %v", err)
	}
	return path
}

func writeFakeResticScript(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "restic")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatalf("write fake restic: %v", err)
	}
	return path
}

func newTestAdapter(t *testing.T, config resticadapter.Config) *resticadapter.Adapter {
	t.Helper()

	originalBinary := config.Binary
	config.Binary = writeFakeResticScript(t, `
if test "$1" = "version"; then
  printf '%s\n' '{"message_type":"version","version":"0.19.1"}'
  exit 0
fi
exec `+shellQuote(originalBinary)+` "$@"
`)
	adapter, err := resticadapter.New(config, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
		return []byte("fixture-password"), nil
	}))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if _, err := adapter.Capabilities(context.Background()); err != nil {
		t.Fatalf("preflight test adapter: %v", err)
	}
	return adapter
}

func newS3SnapshotTestAdapter(t *testing.T) (*resticadapter.Adapter, *resticadapter.S3Credentials, *drill.CredentialReference, string) {
	t.Helper()
	credentialTempDir := t.TempDir()
	binary := writeFakeResticScript(t, `
if test "${1:-}" = "version"; then
  printf '%s\n' '{"message_type":"version","version":"0.19.1"}'
  exit 0
fi
grep -q '^aws_access_key_id = snapshot-access-value$' "$AWS_SHARED_CREDENTIALS_FILE" || exit 91
grep -q '^aws_secret_access_key = snapshot-secret-value$' "$AWS_SHARED_CREDENTIALS_FILE" || exit 92
grep -q '^aws_session_token = snapshot-session-value$' "$AWS_SHARED_CREDENTIALS_FILE" || exit 93
printf '%s\n' '[]'
`)
	sessionToken := &drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "SNAPSHOT_SESSION"}
	credentials := &resticadapter.S3Credentials{
		AccessKeyID:     drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "SNAPSHOT_ACCESS"},
		SecretAccessKey: drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "SNAPSHOT_SECRET"},
		SessionToken:    sessionToken,
	}
	values := map[string]string{
		"PASSWORD":          "snapshot-password-value",
		"SNAPSHOT_ACCESS":   "snapshot-access-value",
		"SNAPSHOT_SECRET":   "snapshot-secret-value",
		"SNAPSHOT_SESSION":  "snapshot-session-value",
		"MUTATED_ACCESS_A":  "snapshot-access-value",
		"MUTATED_ACCESS_B":  "snapshot-access-value",
		"MUTATED_SECRET_A":  "snapshot-secret-value",
		"MUTATED_SECRET_B":  "snapshot-secret-value",
		"MUTATED_SESSION_A": "snapshot-session-value",
		"MUTATED_SESSION_B": "snapshot-session-value",
	}
	adapter, err := resticadapter.New(resticadapter.Config{
		Binary:            binary,
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryS3, Location: "s3:http://minio.test/rehearse/snapshot"},
		Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "PASSWORD"},
		S3Credentials:     credentials,
		CredentialTempDir: credentialTempDir,
	}, credentialResolver(func(_ context.Context, reference drill.CredentialReference) ([]byte, error) {
		value, ok := values[reference.Locator]
		if !ok {
			return nil, errors.New("credential reference unavailable")
		}
		return []byte(value), nil
	}))
	if err != nil {
		t.Fatalf("new snapshot adapter: %v", err)
	}
	if _, err := adapter.Capabilities(context.Background()); err != nil {
		t.Fatalf("preflight snapshot adapter: %v", err)
	}
	return adapter, credentials, sessionToken, credentialTempDir
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func assertPreflightRequired(t *testing.T, err error, operation string) {
	t.Helper()

	var failure *source.Failure
	if !errors.As(err, &failure) || failure.Kind != source.FailureUnsupported || failure.Operation != operation || failure.SafeHint != "restic capability preflight is required" {
		t.Fatalf("failure = %+v, want %s preflight-required unsupported failure", failure, operation)
	}
}

func assertDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read %s: %v", directory, err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %s is not empty: %v", directory, entries)
	}
}

func waitForPath(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat %s: %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(time.Millisecond)
	}
}

func hasSourceFailure(err error, kind source.FailureKind, safeHint string) bool {
	if failure, ok := err.(*source.Failure); ok && failure.Kind == kind && failure.SafeHint == safeHint {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, nested := range joined.Unwrap() {
			if hasSourceFailure(nested, kind, safeHint) {
				return true
			}
		}
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return hasSourceFailure(wrapped.Unwrap(), kind, safeHint)
	}
	return false
}
