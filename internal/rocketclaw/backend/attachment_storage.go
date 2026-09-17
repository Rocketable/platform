package backend

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type attachmentStorage interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
}

type filesystemAttachments struct{ path string }

func (s filesystemAttachments) Put(_ context.Context, id string, data []byte) error {
	if err := os.MkdirAll(s.path, 0o700); err != nil {
		return fmt.Errorf("create attachment directory: %w", err)
	}

	root, err := os.OpenRoot(s.path)
	if err != nil {
		return fmt.Errorf("open attachment directory: %w", err)
	}

	defer func() { _ = root.Close() }()

	temporary := ".pending-" + rand.Text()

	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create pending attachment: %w", err)
	}
	defer func() { _ = root.Remove(temporary) }()

	_, err = file.Write(data)
	if err = errors.Join(err, file.Sync(), file.Close()); err != nil {
		return fmt.Errorf("write pending attachment: %w", err)
	}
	// Link publishes the complete file atomically and never replaces an original.
	if err := root.Link(temporary, id); err != nil {
		return fmt.Errorf("publish attachment: %w", err)
	}

	return nil
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

func (s s3Attachments) Put(ctx context.Context, id string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &id, Body: bytes.NewReader(data), IfNoneMatch: new("*")})
	if err != nil {
		return fmt.Errorf("put S3 attachment: %w", err)
	}

	return nil
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
