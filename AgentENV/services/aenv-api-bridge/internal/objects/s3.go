// Package objects transfers immutable catalog layers through S3-compatible
// storage. Catalog reservations must precede upload and survive unknown results.
package objects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agentenv/services/aenv-api-bridge/internal/catalog"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type client interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}
type uploader interface {
	Upload(context.Context, *s3.PutObjectInput, ...func(*manager.Uploader)) (*manager.UploadOutput, error)
}
type Store struct {
	client         client
	uploader       uploader
	bucket, prefix string
}

func New(c *s3.Client, bucket, prefix string) (*Store, error) {
	if c == nil || bucket == "" || strings.ContainsAny(bucket, "/\\\x00\r\n") || strings.ContainsAny(prefix, "\\\x00\r\n") || strings.HasPrefix(prefix, "/") {
		return nil, fmt.Errorf("invalid S3 location")
	}
	for _, part := range strings.Split(prefix, "/") {
		if part == ".." || part == "." {
			return nil, fmt.Errorf("invalid S3 prefix")
		}
	}
	return &Store{client: c, uploader: manager.NewUploader(c, func(u *manager.Uploader) { u.PartSize = 32 << 20; u.Concurrency = 2 }), bucket: bucket, prefix: strings.TrimSuffix(prefix, "/")}, nil
}
func (s *Store) key(layer catalog.Layer) (string, error) {
	if layer.Size < 0 {
		return "", fmt.Errorf("negative layer size")
	}
	key, err := catalog.ObjectKey(layer.Digest)
	if err != nil {
		return "", err
	}
	if s.prefix != "" {
		key = s.prefix + "/" + key
	}
	return key, nil
}

// Upload checks the complete local file before publishing its content address.
// The caller must keep that file immutable for the duration of this operation.
func (s *Store) Upload(ctx context.Context, layer catalog.Layer, path string) error {
	key, err := s.key(layer)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != layer.Size {
		return fmt.Errorf("layer file size or type mismatch")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != layer.Digest {
		return fmt.Errorf("layer digest mismatch")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = s.uploader.Upload(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: file, ContentLength: aws.Int64(layer.Size), Metadata: map[string]string{"sha256": layer.Digest}})
	return err
}

// Download verifies bytes before atomically publishing the local file. A
// truncated response, excess bytes, or a hash mismatch never replaces dest.
func (s *Store) Download(ctx context.Context, layer catalog.Layer, dest string) error {
	key, err := s.key(layer)
	if err != nil {
		return err
	}
	response, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}
	if response == nil || response.Body == nil {
		return fmt.Errorf("missing S3 object body")
	}
	defer response.Body.Close()
	if response.ContentLength != nil && *response.ContentLength != layer.Size {
		return fmt.Errorf("remote layer size mismatch")
	}
	file, err := os.CreateTemp(filepath.Dir(dest), ".aenv-layer-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	n, err := io.CopyN(io.MultiWriter(file, hash), response.Body, layer.Size)
	if err != nil || n != layer.Size {
		return fmt.Errorf("incomplete layer: %w", err)
	}
	extra := make([]byte, 1)
	nextra, err := io.ReadFull(response.Body, extra)
	if nextra != 0 || err != io.EOF {
		return fmt.Errorf("excess or incomplete object response")
	}
	if hex.EncodeToString(hash.Sum(nil)) != layer.Digest {
		return fmt.Errorf("remote layer digest mismatch")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), dest); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Delete implements catalog.Deleter; only canonical content-addressed keys
// under this Store's fixed prefix are accepted.
func (s *Store) Delete(ctx context.Context, key string) error {
	if len(key) != len("sha256/aa/")+64 {
		return fmt.Errorf("invalid catalog object key")
	}
	digest := key[len(key)-64:]
	expected, err := catalog.ObjectKey(digest)
	if err != nil || expected != key {
		return fmt.Errorf("invalid catalog object key")
	}
	if s.prefix != "" {
		key = s.prefix + "/" + key
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}
