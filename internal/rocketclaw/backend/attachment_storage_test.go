package backend

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestAttachmentStorage(t *testing.T) {
	for _, driver := range []config.AttachmentDriver{config.FilesystemAttachments, config.S3Attachments} {
		t.Run(string(driver), func(t *testing.T) {
			dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, err)

			cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WorkDir: ".femtoclaw", Attachments: config.AttachmentsConfig{Driver: driver}}
			if driver == config.S3Attachments {
				cfg.Attachments.BucketARN = "arn:aws:s3:::originals"

				t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
				t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "credentials"))
				t.Setenv("AWS_ACCESS_KEY_ID", "test")
				t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
				t.Setenv("AWS_REGION", "us-east-1")
				t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
			}

			service, err := NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, service.Stop()) })

			if driver == config.S3Attachments {
				var mu sync.Mutex

				objects := map[string][]byte{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Contains(t, r.Header.Get("Authorization"), "/us-east-1/s3/aws4_request")
					assert.True(t, strings.HasPrefix(r.URL.Path, "/originals/"))
					mu.Lock()
					defer mu.Unlock()

					switch r.Method {
					case http.MethodPut:
						assert.Equal(t, "*", r.Header.Get("If-None-Match"))

						if _, exists := objects[r.URL.Path]; exists {
							w.WriteHeader(http.StatusPreconditionFailed)
							_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)

							return
						}

						data, err := io.ReadAll(r.Body)
						assert.NoError(t, err)

						objects[r.URL.Path] = data
					case http.MethodGet:
						data, exists := objects[r.URL.Path]
						if !exists {
							w.WriteHeader(http.StatusNotFound)
							_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)

							return
						}

						_, _ = w.Write(data)
					default:
						t.Errorf("unexpected S3 request: %s", r.Method)
						w.WriteHeader(http.StatusMethodNotAllowed)
					}
				}))
				t.Cleanup(server.Close)

				storage := service.attachments.(s3Attachments)
				require.Equal(t, "originals", storage.bucket)
				storage.client = s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: new(server.URL), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1})
				service.attachments = storage
			}

			// Each contender has different bytes and metadata; the same transaction must win both.
			var writers errgroup.Group
			for i := range 8 {
				writers.Go(func() error {
					attachment := protocol.OutboundAttachment{ID: "shared", Name: strconv.Itoa(i), Data: bytes.Repeat([]byte{byte(i)}, i+1)}
					return service.SaveAttachment(t.Context(), "producer", &attachment, true)
				})
			}

			require.NoError(t, writers.Wait())
			stored, err := service.LoadAttachment(t.Context(), "producer", "shared", true)
			require.NoError(t, err)
			require.Equal(t, strconv.Itoa(int(stored.Data[0])), stored.Name)
			require.Equal(t, int64(len(stored.Data)), stored.Size)
			require.Equal(t, bytes.Repeat(stored.Data[:1], int(stored.Data[0])+1), stored.Data)
			require.NoError(t, service.SaveAttachment(t.Context(), "other", &protocol.OutboundAttachment{ID: "shared", Name: "changed", Data: []byte("changed")}, false))
			again, err := service.LoadAttachment(t.Context(), "producer", "shared", true)
			require.NoError(t, err)
			require.Equal(t, stored, again)
			_, err = service.LoadAttachment(t.Context(), "other", "shared", false)
			require.ErrorIs(t, err, sql.ErrNoRows)

			// An earlier storage success followed by a failed DB commit leaves an orphan,
			// which must never be paired with this new name, scope, size, or upload flag.
			require.NoError(t, service.attachments.Put(t.Context(), "orphan", []byte("original")))
			err = service.SaveAttachment(t.Context(), "producer", &protocol.OutboundAttachment{ID: "orphan", Name: "replacement", Data: []byte("different")}, true)
			require.Error(t, err)

			if driver == config.S3Attachments {
				require.ErrorContains(t, err, "PreconditionFailed")
			} else {
				require.ErrorIs(t, err, os.ErrExist)
			}

			_, err = service.AttachmentMetadata(t.Context(), "producer", "orphan", false)
			require.ErrorIs(t, err, sql.ErrNoRows)
			original, err := service.attachments.Get(t.Context(), "orphan")
			require.NoError(t, err)
			require.Equal(t, []byte("original"), original)
			_, err = service.attachments.Get(t.Context(), "missing")
			require.Error(t, err)
		})
	}
}

func TestFilesystemAttachmentsAtomicAndConfined(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	storage := filesystemAttachments{path: root.Name()}
	data := bytes.Repeat([]byte("original"), 1<<18)

	var writers errgroup.Group

	errs := make([]error, 8)
	for i := range errs {
		writers.Go(func() error {
			errs[i] = storage.Put(t.Context(), "snapshot", data)

			got, err := storage.Get(t.Context(), "snapshot")
			if err != nil {
				return err
			}

			if !bytes.Equal(data, got) {
				return errors.New("reader observed a partial original")
			}

			return nil
		})
	}

	require.NoError(t, writers.Wait())

	winners := 0

	for _, err := range errs {
		if err == nil {
			winners++
		} else {
			require.ErrorIs(t, err, os.ErrExist)
		}
	}

	require.Equal(t, 1, winners)

	for _, id := range []string{"../escape", filepath.Join(t.TempDir(), "escape"), "missing/file"} {
		require.Error(t, storage.Put(t.Context(), id, data))
		_, err := storage.Get(t.Context(), id)
		require.Error(t, err)
	}

	outside, err := os.OpenRoot(t.TempDir())

	require.NoError(t, err)
	defer func() { require.NoError(t, outside.Close()) }()

	require.NoError(t, outside.WriteFile("secret", []byte("outside"), 0o600))
	require.NoError(t, root.Symlink(filepath.Join(outside.Name(), "secret"), "symlink"))
	_, err = storage.Get(t.Context(), "symlink")
	require.Error(t, err)
	require.Error(t, storage.Put(t.Context(), "symlink", data))

	entries, err := fs.ReadDir(root.FS(), ".")
	require.NoError(t, err)
	require.Len(t, entries, 2, "failed and conflicting publications must remove pending files")
	require.NoError(t, root.Remove("symlink"))
	require.NoError(t, root.Remove("snapshot"))
	// A non-directory storage path must fail rather than publish metadata.
	parent, err := os.OpenRoot(filepath.Dir(storage.path))

	require.NoError(t, err)
	defer func() { require.NoError(t, parent.Close()) }()

	require.NoError(t, parent.Remove(filepath.Base(storage.path)))
	require.NoError(t, parent.WriteFile(filepath.Base(storage.path), []byte("collision"), 0o600))
	require.Error(t, storage.Put(context.Background(), "new", data))
	_, err = storage.Get(context.Background(), "new")
	require.Error(t, err)
}
