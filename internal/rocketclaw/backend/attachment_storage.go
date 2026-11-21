package backend

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type attachmentStorage interface {
	Put(context.Context, string, io.Reader) (int64, error)
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

type filesystemAttachments struct{ path string }

func (s filesystemAttachments) Put(_ context.Context, id string, body io.Reader) (int64, error) {
	if err := os.MkdirAll(s.path, 0o700); err != nil {
		return 0, fmt.Errorf("create attachment directory: %w", err)
	}

	root, err := os.OpenRoot(s.path)
	if err != nil {
		return 0, fmt.Errorf("open attachment directory: %w", err)
	}

	defer func() { _ = root.Close() }()

	temporary := ".pending-" + rand.Text()

	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create pending attachment: %w", err)
	}
	defer func() { _ = root.Remove(temporary) }()

	size, err := io.Copy(file, body)
	if err = errors.Join(err, file.Sync(), file.Close()); err != nil {
		return 0, fmt.Errorf("write pending attachment: %w", err)
	}

	if err := root.Link(temporary, id); err != nil {
		return 0, fmt.Errorf("publish attachment: %w", err)
	}

	return size, nil
}

func (s filesystemAttachments) Get(_ context.Context, id string) ([]byte, error) {
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, fmt.Errorf("open attachment directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	data, err := root.ReadFile(id)
	if err != nil {
		return nil, fmt.Errorf("read attachment file: %w", err)
	}

	return data, nil
}

type s3Attachments struct {
	client *s3.Client
	bucket string
}

func (s filesystemAttachments) Delete(_ context.Context, id string) error {
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return fmt.Errorf("open deleted attachment storage: %w", err)
	}
	defer func() { _ = root.Close() }()

	if err := root.Remove(id); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete attachment: %w", err)
	}

	return nil
}

func (s s3Attachments) Delete(ctx context.Context, id string) error {
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &id}); err != nil {
		return fmt.Errorf("delete S3 attachment: %w", err)
	}

	return nil
}

func (s s3Attachments) Put(ctx context.Context, id string, body io.Reader) (int64, error) {
	uploaded, err := transfermanager.New(s.client, func(o *transfermanager.Options) { o.FailTimeout = time.Minute }).UploadObject(ctx, &transfermanager.UploadObjectInput{Bucket: &s.bucket, Key: &id, Body: body, IfNoneMatch: new("*")})
	if err != nil {
		return 0, fmt.Errorf("put S3 attachment: %w", err)
	}

	return *uploaded.ContentLength, nil
}

func (s s3Attachments) Get(ctx context.Context, id string) ([]byte, error) {
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &id})
	if err != nil {
		return nil, fmt.Errorf("get S3 attachment: %w", err)
	}
	defer func() { _ = object.Body.Close() }()

	data, err := io.ReadAll(object.Body)
	if err != nil {
		return nil, fmt.Errorf("read S3 attachment: %w", err)
	}

	return data, nil
}
