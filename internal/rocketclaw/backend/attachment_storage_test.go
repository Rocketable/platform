package backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

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

			var (
				mu         sync.Mutex
				cancelSave context.CancelFunc = func() {}
				parts                         = map[string]map[int][]byte{}
			)

			inFlight := func() int64 {
				var size int64

				if driver == config.S3Attachments {
					mu.Lock()
					defer mu.Unlock()

					for _, upload := range parts {
						for _, part := range upload {
							size += int64(len(part))
						}
					}

					return size
				}

				entries, _ := os.ReadDir(service.attachments.(filesystemAttachments).path)
				for _, entry := range entries {
					if info, err := entry.Info(); err == nil && strings.HasPrefix(entry.Name(), ".pending-") {
						size += info.Size()
					}
				}

				return size
			}

			if driver == config.S3Attachments {
				objects := map[string][]byte{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Contains(t, r.Header.Get("Authorization"), "/us-east-1/s3/aws4_request")
					assert.True(t, strings.HasPrefix(r.URL.Path, "/originals/"))
					mu.Lock()
					defer mu.Unlock()

					query := r.URL.Query()
					upload := query.Get("uploadId")

					switch {
					case r.Method == http.MethodPost && query.Has("uploads"):
						parts[r.URL.Path] = map[int][]byte{}
						_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>`+r.URL.Path+`</UploadId></InitiateMultipartUploadResult>`)
					case r.Method == http.MethodPut && upload != "":
						number, err := strconv.Atoi(query.Get("partNumber"))
						assert.NoError(t, err)
						assert.NotEmpty(t, r.Header.Get("X-Amz-Checksum-Crc32"))

						data, err := io.ReadAll(r.Body)
						if _, open := parts[upload]; err != nil || !open {
							w.WriteHeader(http.StatusNotFound)
							_, _ = io.WriteString(w, `<Error><Code>NoSuchUpload</Code></Error>`)

							return
						}

						parts[upload][number] = data
						w.Header().Set("ETag", `"`+strconv.Itoa(number)+`"`)
					case r.Method == http.MethodPost && upload != "":
						assert.Equal(t, "*", r.Header.Get("If-None-Match"))

						if _, exists := objects[r.URL.Path]; exists {
							w.WriteHeader(http.StatusPreconditionFailed)
							_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)

							return
						}

						var data []byte
						for number := 1; number <= len(parts[upload]); number++ {
							data = append(data, parts[upload][number]...)
						}

						objects[r.URL.Path] = data

						delete(parts, upload)

						_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
					case r.Method == http.MethodDelete && upload != "":
						delete(parts, upload)
						w.WriteHeader(http.StatusNoContent)
					}

					if upload != "" || query.Has("uploads") {
						return
					}

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

						if strings.HasSuffix(r.URL.Path, "lost-reply") {
							w.WriteHeader(http.StatusInternalServerError)
						}
					case http.MethodGet:
						data, exists := objects[r.URL.Path]
						if !exists {
							w.WriteHeader(http.StatusNotFound)
							_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)

							return
						}

						_, _ = w.Write(data)
					case http.MethodDelete:
						cancelSave()

						if strings.HasSuffix(r.URL.Path, "undeletable") {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}

						delete(objects, r.URL.Path)
						w.WriteHeader(http.StatusNoContent)
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

			var writers errgroup.Group
			for i := range 8 {
				writers.Go(func() error {
					attachment := protocol.OutboundAttachment{ID: "shared", Name: strconv.Itoa(i), Data: bytes.Repeat([]byte{byte(i)}, i+1)}
					return service.SaveAttachment(t.Context(), "producer", "", &attachment, true)
				})
			}

			require.NoError(t, writers.Wait())
			stored, err := service.LoadAttachment(t.Context(), "producer", "shared", true)
			require.NoError(t, err)
			require.Equal(t, strconv.Itoa(int(stored.Data[0])), stored.Name)
			require.Equal(t, int64(len(stored.Data)), stored.Size)
			require.Equal(t, bytes.Repeat(stored.Data[:1], int(stored.Data[0])+1), stored.Data)
			require.NoError(t, service.SaveAttachment(t.Context(), "other", "", &protocol.OutboundAttachment{ID: "shared", Name: "changed", Data: []byte("changed")}, false))
			again, err := service.LoadAttachment(t.Context(), "producer", "shared", true)
			require.NoError(t, err)
			require.Equal(t, stored, again)
			_, err = service.LoadAttachment(t.Context(), "other", "shared", false)
			require.ErrorIs(t, err, sql.ErrNoRows)

			_, err = service.attachments.Put(t.Context(), "orphan", strings.NewReader("original"))
			require.NoError(t, err)
			err = service.SaveAttachment(t.Context(), "producer", "", &protocol.OutboundAttachment{ID: "orphan", Name: "replacement", Data: []byte("different")}, true)
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

			const (
				uploadBytes = 24<<20 + 7
				headBytes   = 20 << 20
			)

			reachesStorage := readHook(func() {
				require.Eventually(t, func() bool { return inFlight() >= 8<<20 }, 10*time.Second, time.Millisecond, "the start of the upload reaches storage before the rest is read")
			})

			large := protocol.OutboundAttachment{ID: "large", Name: "large.bin"}
			require.NoError(t, service.SaveUpload(t.Context(), "producer", &large, io.MultiReader(io.LimitReader(rand.NewChaCha8([32]byte{1}), headBytes), reachesStorage, io.LimitReader(rand.NewChaCha8([32]byte{2}), uploadBytes-headBytes))))
			require.Zero(t, inFlight())

			want := sha256.New()
			_, err = io.Copy(want, io.MultiReader(io.LimitReader(rand.NewChaCha8([32]byte{1}), headBytes), io.LimitReader(rand.NewChaCha8([32]byte{2}), uploadBytes-headBytes)))
			require.NoError(t, err)

			stored, err = service.LoadAttachment(t.Context(), "producer", "large", true)
			require.NoError(t, err)
			require.Equal(t, int64(uploadBytes), stored.Size)
			got := sha256.Sum256(stored.Data)
			require.Equal(t, want.Sum(nil), got[:])

			abortCtx, abort := context.WithCancel(t.Context())
			err = service.SaveUpload(abortCtx, "producer", &protocol.OutboundAttachment{ID: "aborted", Name: "aborted.bin"}, io.MultiReader(io.LimitReader(rand.NewChaCha8([32]byte{3}), headBytes), reachesStorage, readHook(abort), iotest.ErrReader(errors.New("client went away"))))
			require.ErrorContains(t, err, "client went away")
			require.Zero(t, inFlight(), "an aborted upload leaves no pending file or multipart upload")
			_, err = service.attachments.Get(t.Context(), "aborted")
			require.Error(t, err)
			_, err = service.AttachmentMetadata(t.Context(), "producer", "aborted", false)
			require.ErrorIs(t, err, sql.ErrNoRows)

			if driver == config.S3Attachments {
				require.Error(t, service.SaveUpload(t.Context(), "producer", &protocol.OutboundAttachment{ID: "lost-reply", Name: "lost"}, strings.NewReader("stored anyway")))
				_, err = service.attachments.Get(t.Context(), "lost-reply")
				require.Error(t, err, "an object stored before a failed reply is removed")
			}

			_, err = service.db.ExecContext(t.Context(), `CREATE FUNCTION reject_attachment_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'attachment commit unavailable'; END $$;
CREATE CONSTRAINT TRIGGER reject_attachment_commit AFTER INSERT ON attachments DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.id LIKE 'uncommitted%') EXECUTE FUNCTION reject_attachment_commit()`)
			require.NoError(t, err)

			uncommitted, queued := []string{"uncommitted", "uncommitted-upload"}, []string(nil)
			if driver == config.S3Attachments {
				uncommitted, queued = append(uncommitted, "uncommitted-undeletable", "uncommitted-upload-undeletable"), []string{"uncommitted-undeletable", "uncommitted-upload-undeletable"}
			}

			for _, id := range uncommitted {
				ctx, cancel := context.WithCancel(t.Context())

				mu.Lock()
				cancelSave = cancel
				mu.Unlock()

				if strings.Contains(id, "upload") {
					err = service.SaveUpload(ctx, "producer", &protocol.OutboundAttachment{ID: id, Name: id}, strings.NewReader(id))
				} else {
					err = service.SaveAttachment(ctx, "producer", "", &protocol.OutboundAttachment{ID: id, Name: id, Data: []byte(id)}, true)
				}

				require.ErrorContains(t, err, "attachment commit unavailable")
				_, err = service.AttachmentMetadata(t.Context(), "producer", id, false)
				require.ErrorIs(t, err, sql.ErrNoRows)
			}

			for _, id := range []string{"uncommitted", "uncommitted-upload"} {
				_, err = service.attachments.Get(t.Context(), id)
				require.Error(t, err, "a failed commit must not leave the stored file behind")
			}

			require.NoError(t, service.SaveAttachment(t.Context(), "producer", "", &protocol.OutboundAttachment{ID: "committed", Name: "committed", Data: []byte("committed")}, true))
			require.NoError(t, service.discardUnsavedAttachment(t.Context(), "committed"))
			kept, err := service.attachments.Get(t.Context(), "committed")
			require.NoError(t, err, "a commit error after the row landed must keep its file")
			require.Equal(t, []byte("committed"), kept)

			_, err = service.db.ExecContext(t.Context(), `ALTER TABLE attachments RENAME TO attachments_unavailable`)
			require.NoError(t, err)
			require.ErrorContains(t, service.discardUnsavedAttachment(t.Context(), "committed"), "check unsaved attachment")
			kept, err = service.attachments.Get(t.Context(), "committed")
			require.NoError(t, err, "an unconfirmed failure must keep the file")
			require.Equal(t, []byte("committed"), kept)

			pending, err := queryStrings(t.Context(), service.db, `SELECT id FROM pending_attachment_deletions ORDER BY id`, "pending attachment deletions")
			require.NoError(t, err)
			require.Equal(t, queued, pending)
		})
	}
}

type readHook func()

func (h readHook) Read([]byte) (int, error) {
	h()
	return 0, io.EOF
}

func TestFilesystemAttachmentsAtomicAndConfined(t *testing.T) {
	t.Parallel()

	root, err := os.OpenRoot(t.TempDir())

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	storage := filesystemAttachments{path: root.Name()}
	data := bytes.Repeat([]byte("original"), 1<<18)

	var writers errgroup.Group

	errs := make([]error, 8)
	for i := range errs {
		writers.Go(func() error {
			_, errs[i] = storage.Put(t.Context(), "snapshot", bytes.NewReader(data))

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
		_, err := storage.Put(t.Context(), id, bytes.NewReader(data))
		require.Error(t, err)
		_, err = storage.Get(t.Context(), id)
		require.Error(t, err)
	}

	outside, err := os.OpenRoot(t.TempDir())

	require.NoError(t, err)
	defer func() { require.NoError(t, outside.Close()) }()

	require.NoError(t, outside.WriteFile("secret", []byte("outside"), 0o600))
	require.NoError(t, root.Symlink(filepath.Join(outside.Name(), "secret"), "symlink"))
	_, err = storage.Get(t.Context(), "symlink")
	require.Error(t, err)
	_, err = storage.Put(t.Context(), "symlink", bytes.NewReader(data))
	require.Error(t, err)

	entries, err := fs.ReadDir(root.FS(), ".")
	require.NoError(t, err)
	require.Len(t, entries, 2, "failed and conflicting publications must remove pending files")
	require.NoError(t, root.Remove("symlink"))
	require.NoError(t, root.Remove("snapshot"))

	parent, err := os.OpenRoot(filepath.Dir(storage.path))

	require.NoError(t, err)
	defer func() { require.NoError(t, parent.Close()) }()

	require.NoError(t, parent.Remove(filepath.Base(storage.path)))
	require.NoError(t, parent.WriteFile(filepath.Base(storage.path), []byte("collision"), 0o600))
	_, err = storage.Put(context.Background(), "new", bytes.NewReader(data))
	require.Error(t, err)
	_, err = storage.Get(context.Background(), "new")
	require.Error(t, err)
}
