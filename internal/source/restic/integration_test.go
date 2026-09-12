//go:build integration

package restic_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/source"
	resticadapter "github.com/azizu06/rehearse/internal/source/restic"
	minioclient "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/testcontainers/testcontainers-go"
	miniocontainer "github.com/testcontainers/testcontainers-go/modules/minio"
)

func TestLocalRepositoryListAndAcquireAreReadOnlyAtTheRealResticBoundary(t *testing.T) {
	ctx := context.Background()
	binary := realResticBinary(t)
	repository := t.TempDir()
	passwordFile := writePasswordFile(t, "real-local-boundary-password")
	sourceDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDirectory, "orders.db"), []byte("seeded-order-42"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	runResticSetup(t, binary, repository, passwordFile, "init")
	runResticSetup(t, binary, repository, passwordFile, "backup", sourceDirectory, "--host", "boundary-host", "--tag", "integration")
	before := directoryDigest(t, repository)

	adapter, err := resticadapter.New(resticadapter.Config{
		Binary:            binary,
		Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: repository},
		Password:          drill.CredentialReference{Provider: drill.CredentialFile, Locator: passwordFile},
		CredentialTempDir: t.TempDir(),
	}, credentialResolver(func(_ context.Context, reference drill.CredentialReference) ([]byte, error) {
		return os.ReadFile(reference.Locator)
	}))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	capabilities, err := adapter.Capabilities(ctx)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !strings.HasPrefix(capabilities.AdapterVersion, "restic/") {
		t.Fatalf("unexpected version: %+v", capabilities)
	}
	points, err := adapter.ListRecoveryPoints(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(points) != 1 || points[0].Host != "boundary-host" || len(points[0].Tags) != 1 || points[0].Tags[0] != "integration" {
		t.Fatalf("unexpected recovery points: %+v", points)
	}
	var progress []source.Progress
	artifact, err := adapter.Acquire(ctx, source.AcquireRequest{
		RecoveryPointID: points[0].ID,
		Workspace:       t.TempDir(),
		Report: func(event source.Progress) {
			progress = append(progress, event)
		},
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	restoredFile := findFile(t, artifact.Path, "orders.db")
	contents, err := os.ReadFile(restoredFile)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(contents) != "seeded-order-42" {
		t.Fatalf("restored contents = %q", contents)
	}
	if len(progress) == 0 {
		t.Fatal("real restic restore reported no progress")
	}
	after := directoryDigest(t, repository)
	if before != after {
		t.Fatalf("source repository changed during list/acquire: before=%s after=%s", before, after)
	}
}

func TestRealResticLocalFailurePathsAreTypedRedactedAndClean(t *testing.T) {
	binary := realResticBinary(t)
	validRepository := t.TempDir()
	passwordFile := writePasswordFile(t, "real-boundary-secret-marker")
	runResticSetup(t, binary, validRepository, passwordFile, "init")
	corruptRepository := t.TempDir()
	corruptPasswordFile := writePasswordFile(t, "corrupt-boundary-secret-marker")
	runResticSetup(t, binary, corruptRepository, corruptPasswordFile, "init")
	configPath := filepath.Join(corruptRepository, "config")
	if err := os.Chmod(configPath, 0o600); err != nil {
		t.Fatalf("make repository config writable: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("corrupt-private-input"), 0o600); err != nil {
		t.Fatalf("corrupt repository config: %v", err)
	}

	tests := []struct {
		name       string
		repository string
		password   string
		wantKind   source.FailureKind
		private    string
	}{
		{
			name:       "wrong password",
			repository: validRepository,
			password:   "wrong-private-password",
			wantKind:   source.FailureAuthentication,
			private:    "wrong-private-password",
		},
		{
			name:       "missing repository",
			repository: filepath.Join(t.TempDir(), "missing-private-repository"),
			password:   "missing-private-password",
			wantKind:   source.FailureRepositoryMissing,
			private:    "missing-private-repository",
		},
		{
			name:       "corrupt repository input",
			repository: corruptRepository,
			password:   "corrupt-boundary-secret-marker",
			wantKind:   source.FailureProcess,
			private:    "corrupt-private-input",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentialTempDir := t.TempDir()
			adapter, err := resticadapter.New(resticadapter.Config{
				Binary:            binary,
				Repository:        resticadapter.Repository{Kind: resticadapter.RepositoryLocal, Location: test.repository},
				Password:          drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "BOUNDARY_PASSWORD"},
				CredentialTempDir: credentialTempDir,
			}, credentialResolver(func(context.Context, drill.CredentialReference) ([]byte, error) {
				return []byte(test.password), nil
			}))
			if err != nil {
				t.Fatalf("new adapter: %v", err)
			}
			if _, err := adapter.Capabilities(context.Background()); err != nil {
				t.Fatalf("preflight adapter: %v", err)
			}

			_, err = adapter.ListRecoveryPoints(context.Background())
			var failure *source.Failure
			if !errors.As(err, &failure) || failure.Kind != test.wantKind {
				t.Fatalf("failure = %+v, want kind %q", failure, test.wantKind)
			}
			if strings.Contains(err.Error(), test.private) || strings.Contains(err.Error(), test.repository) || strings.Contains(err.Error(), test.password) {
				t.Fatalf("private boundary data leaked through error: %v", err)
			}
			assertDirectoryEmpty(t, credentialTempDir)
		})
	}
}

func TestS3CompatibleRepositoryListAndAcquireAreReadOnlyAtTheMinIOBoundary(t *testing.T) {
	ctx := context.Background()
	binary := realResticBinary(t)
	const (
		accessKey = "rehearse-access"
		secretKey = "rehearse-secret-key"
		bucket    = "rehearse-boundary"
	)
	container, err := miniocontainer.Run(
		ctx,
		"quay.io/minio/minio:RELEASE.2024-01-16T16-07-38Z",
		miniocontainer.WithUsername(accessKey),
		miniocontainer.WithPassword(secretKey),
	)
	if err != nil {
		t.Fatalf("start MinIO: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Errorf("terminate MinIO: %v", err)
		}
	})
	connection, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("MinIO connection string: %v", err)
	}
	endpointURL := connection
	if !strings.Contains(endpointURL, "://") {
		endpointURL = "http://" + endpointURL
	}
	endpoint, err := url.Parse(endpointURL)
	if err != nil {
		t.Fatalf("parse MinIO endpoint: %v", err)
	}
	client, err := minioclient.New(endpoint.Host, &minioclient.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: endpoint.Scheme == "https",
	})
	if err != nil {
		t.Fatalf("new MinIO client: %v", err)
	}
	if err := client.MakeBucket(ctx, bucket, minioclient.MakeBucketOptions{}); err != nil {
		t.Fatalf("create MinIO bucket: %v", err)
	}
	repository := "s3:" + strings.TrimSuffix(endpointURL, "/") + "/" + bucket + "/repository"
	passwordFile := writePasswordFile(t, "real-s3-boundary-password")
	sourceDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDirectory, "orders.db"), []byte("seeded-s3-order-84"), 0o600); err != nil {
		t.Fatalf("seed S3 source: %v", err)
	}
	runResticS3Setup(t, binary, repository, passwordFile, accessKey, secretKey, "init")
	runResticS3Setup(t, binary, repository, passwordFile, accessKey, secretKey, "backup", sourceDirectory, "--host", "minio-boundary", "--tag", "s3-integration")
	before := minioObjectDigest(t, ctx, client, bucket)

	values := map[string][]byte{
		"PASSWORD":   []byte("real-s3-boundary-password"),
		"ACCESS_KEY": []byte(accessKey),
		"SECRET_KEY": []byte(secretKey),
	}
	credentialTempDir := t.TempDir()
	adapter, err := resticadapter.New(resticadapter.Config{
		Binary: binary,
		Repository: resticadapter.Repository{
			Kind:     resticadapter.RepositoryS3,
			Location: repository,
		},
		Password: drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "PASSWORD"},
		S3Credentials: &resticadapter.S3Credentials{
			AccessKeyID:     drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "ACCESS_KEY"},
			SecretAccessKey: drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "SECRET_KEY"},
		},
		CredentialTempDir: credentialTempDir,
	}, credentialResolver(func(_ context.Context, reference drill.CredentialReference) ([]byte, error) {
		return append([]byte(nil), values[reference.Locator]...), nil
	}))
	if err != nil {
		t.Fatalf("new S3 adapter: %v", err)
	}
	if _, err := adapter.Capabilities(ctx); err != nil {
		t.Fatalf("preflight S3 adapter: %v", err)
	}
	points, err := adapter.ListRecoveryPoints(ctx)
	if err != nil {
		t.Fatalf("list S3 recovery points: %v", err)
	}
	if len(points) != 1 || points[0].Host != "minio-boundary" {
		t.Fatalf("unexpected S3 recovery points: %+v", points)
	}
	artifact, err := adapter.Acquire(ctx, source.AcquireRequest{
		RecoveryPointID: points[0].ID,
		Workspace:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("acquire S3 snapshot: %v", err)
	}
	contents, err := os.ReadFile(findFile(t, artifact.Path, "orders.db"))
	if err != nil {
		t.Fatalf("read S3-restored file: %v", err)
	}
	if string(contents) != "seeded-s3-order-84" {
		t.Fatalf("S3-restored contents = %q", contents)
	}
	assertDirectoryEmpty(t, credentialTempDir)

	const (
		wrongAccessKey = "private-wrong-s3-access-key"
		wrongSecretKey = "private-wrong-s3-secret-key"
	)
	failureCredentialTempDir := t.TempDir()
	failureValues := map[string][]byte{
		"PASSWORD":   []byte("real-s3-boundary-password"),
		"ACCESS_KEY": []byte(wrongAccessKey),
		"SECRET_KEY": []byte(wrongSecretKey),
	}
	failureAdapter, err := resticadapter.New(resticadapter.Config{
		Binary: binary,
		Repository: resticadapter.Repository{
			Kind:     resticadapter.RepositoryS3,
			Location: repository,
		},
		Password: drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "PASSWORD"},
		S3Credentials: &resticadapter.S3Credentials{
			AccessKeyID:     drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "ACCESS_KEY"},
			SecretAccessKey: drill.CredentialReference{Provider: drill.CredentialEnvironment, Locator: "SECRET_KEY"},
		},
		CredentialTempDir: failureCredentialTempDir,
	}, credentialResolver(func(_ context.Context, reference drill.CredentialReference) ([]byte, error) {
		return append([]byte(nil), failureValues[reference.Locator]...), nil
	}))
	if err != nil {
		t.Fatalf("new failing S3 adapter: %v", err)
	}
	if _, err := failureAdapter.Capabilities(ctx); err != nil {
		t.Fatalf("preflight failing S3 adapter: %v", err)
	}
	failureCtx, cancelFailure := context.WithTimeout(ctx, 2*time.Second)
	defer cancelFailure()
	_, err = failureAdapter.ListRecoveryPoints(failureCtx)
	var failure *source.Failure
	if !errors.As(err, &failure) || failure.Kind != source.FailureProcess && failure.Kind != source.FailureTimeout {
		t.Fatalf("failure = %+v, want typed process or timeout failure", failure)
	}
	if strings.Contains(err.Error(), wrongAccessKey) || strings.Contains(err.Error(), wrongSecretKey) || strings.Contains(err.Error(), repository) {
		t.Fatalf("private S3 boundary data leaked through error: %v", err)
	}
	assertDirectoryEmpty(t, failureCredentialTempDir)

	after := minioObjectDigest(t, ctx, client, bucket)
	if before != after {
		t.Fatalf("S3 source repository changed during list/acquire: before=%s after=%s", before, after)
	}
}

func realResticBinary(t *testing.T) string {
	t.Helper()

	if binary := os.Getenv("RESTIC_TEST_BINARY"); binary != "" {
		return binary
	}
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("RESTIC_TEST_BINARY is unset and restic is not installed")
	}
	return binary
}

func writePasswordFile(t *testing.T, password string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(password), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}
	return path
}

func runResticSetup(t *testing.T, binary, repository, passwordFile string, args ...string) {
	t.Helper()

	commandArgs := append([]string{"--json", "--no-cache"}, args...)
	command := exec.Command(binary, commandArgs...)
	command.Env = []string{
		"LANG=C",
		"LC_ALL=C",
		"RESTIC_REPOSITORY=" + repository,
		"RESTIC_PASSWORD_FILE=" + passwordFile,
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restic setup %v: %v\n%s", args, err, output)
	}
}

func runResticS3Setup(t *testing.T, binary, repository, passwordFile, accessKey, secretKey string, args ...string) {
	t.Helper()

	commandArgs := append([]string{"--json", "--no-cache"}, args...)
	command := exec.Command(binary, commandArgs...)
	command.Env = []string{
		"LANG=C",
		"LC_ALL=C",
		"RESTIC_REPOSITORY=" + repository,
		"RESTIC_PASSWORD_FILE=" + passwordFile,
		"AWS_ACCESS_KEY_ID=" + accessKey,
		"AWS_SECRET_ACCESS_KEY=" + secretKey,
		"AWS_EC2_METADATA_DISABLED=true",
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restic S3 setup %v: %v\n%s", args, err, output)
	}
}

func minioObjectDigest(t *testing.T, ctx context.Context, client *minioclient.Client, bucket string) string {
	t.Helper()

	var objects []string
	for object := range client.ListObjects(ctx, bucket, minioclient.ListObjectsOptions{Recursive: true}) {
		if object.Err != nil {
			t.Fatalf("list MinIO objects: %v", object.Err)
		}
		objects = append(objects, object.Key+"\x00"+object.ETag+"\x00"+strconv.FormatInt(object.Size, 10))
	}
	sort.Strings(objects)
	digest := sha256.New()
	for _, object := range objects {
		_, _ = digest.Write([]byte(object))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func directoryDigest(t *testing.T, root string) string {
	t.Helper()

	type entry struct {
		path string
		sum  string
	}
	var entries []entry
	err := filepath.WalkDir(root, func(path string, directoryEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if directoryEntry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(contents)
		entries = append(entries, entry{path: filepath.ToSlash(relative), sum: hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		t.Fatalf("digest repository: %v", err)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].path < entries[right].path })
	digest := sha256.New()
	for _, item := range entries {
		_, _ = digest.Write([]byte(item.path))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(item.sum))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func findFile(t *testing.T, root, name string) string {
	t.Helper()

	var found string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == name {
			if found != "" {
				return errors.New("multiple matching files")
			}
			found = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("find %s: %v", name, err)
	}
	if found == "" {
		t.Fatalf("%s not found under %s", name, root)
	}
	return found
}
