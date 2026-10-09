package objects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"agentenv/services/aenv-api-bridge/internal/catalog"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakeStore struct {
	data             []byte
	key, bucket      string
	err              error
	uploads, deletes int
}

func (f *fakeStore) GetObject(_ context.Context, r *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.key = aws.ToString(r.Key)
	f.bucket = aws.ToString(r.Bucket)
	if f.err != nil {
		return nil, f.err
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(f.data))}, nil
}
func (f *fakeStore) DeleteObject(_ context.Context, r *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deletes++
	f.key = aws.ToString(r.Key)
	return &s3.DeleteObjectOutput{}, f.err
}
func (f *fakeStore) Upload(_ context.Context, r *s3.PutObjectInput, _ ...func(*manager.Uploader)) (*manager.UploadOutput, error) {
	f.uploads++
	f.key = aws.ToString(r.Key)
	f.bucket = aws.ToString(r.Bucket)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	f.data = data
	return &manager.UploadOutput{}, f.err
}
func layerFor(data []byte) catalog.Layer {
	sum := sha256.Sum256(data)
	return catalog.Layer{Digest: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}
func TestImmutableLayerTransferAndDeletion(t *testing.T) {
	data := []byte("portable snapshot bytes")
	layer := layerFor(data)
	fake := &fakeStore{}
	store := &Store{client: fake, uploader: fake, bucket: "snapshots", prefix: "suite"}
	src := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Upload(t.Context(), layer, src); err != nil {
		t.Fatal(err)
	}
	key, _ := catalog.ObjectKey(layer.Digest)
	if fake.key != "suite/"+key || fake.bucket != "snapshots" || !bytes.Equal(fake.data, data) {
		t.Fatal("wrong immutable object")
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err := store.Download(t.Context(), layer, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("wrong restored bytes")
	}
	if err = store.Delete(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if fake.key != "suite/"+key {
		t.Fatal("delete escaped configured prefix")
	}
	if err = store.Delete(t.Context(), "../../another-object"); err == nil || fake.deletes != 1 {
		t.Fatal("unsafe object deletion")
	}
}
func TestCorruptionNeverPublishesOrReplacesDestination(t *testing.T) {
	data := []byte("expected-data")
	layer := layerFor(data)
	fake := &fakeStore{}
	store := &Store{client: fake, uploader: fake, bucket: "snapshots"}
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "restored")
	if err := os.WriteFile(src, []byte("differentdata"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Upload(t.Context(), layer, src); err == nil || fake.uploads != 0 {
		t.Fatal("bad digest reached upload")
	}
	for _, bad := range [][]byte{[]byte("truncated"), []byte("differentdata"), append(append([]byte(nil), data...), 1)} {
		if err := os.WriteFile(dest, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		fake.data = bad
		if err := store.Download(t.Context(), layer, dest); err == nil {
			t.Fatal("corrupt download accepted")
		}
		got, _ := os.ReadFile(dest)
		if string(got) != "keep" {
			t.Fatal("bad download replaced destination")
		}
		matches, _ := filepath.Glob(filepath.Join(dir, ".aenv-layer-*"))
		if len(matches) != 0 {
			t.Fatal("partial download leaked")
		}
	}
	fake.err = errors.New("object store unavailable")
	if err := store.Download(t.Context(), layer, dest); err == nil {
		t.Fatal("remote error swallowed")
	}
}
