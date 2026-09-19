package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
)

// ObjectStore holds artifact bytes under content-addressed keys.
type ObjectStore interface {
	Put(context.Context, []byte) (string, string, error)
	Get(context.Context, string) ([]byte, error)
}

var objectKey = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)

type FileStore struct{ Root string }

func NewFileStore(root string) (*FileStore, error) {
	p, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(p, 0700); e != nil {
		return nil, e
	}
	return &FileStore{p}, nil
}
func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func (s *FileStore) Put(ctx context.Context, data []byte) (string, string, error) {
	if e := ctx.Err(); e != nil {
		return "", "", e
	}
	hash := digest(data)
	key := hash + ".json"
	f, e := os.CreateTemp(s.Root, ".pending-")
	if e != nil {
		return "", "", e
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, e = f.Write(data); e != nil {
		_ = f.Close()
		return "", "", e
	}
	if e = f.Sync(); e != nil {
		_ = f.Close()
		return "", "", e
	}
	if e = f.Close(); e != nil {
		return "", "", e
	}
	e = os.Link(name, filepath.Join(s.Root, key))
	if e != nil && !errors.Is(e, os.ErrExist) {
		return "", "", e
	}
	existing, e := s.Get(ctx, key)
	if e != nil {
		return "", "", e
	}
	if !bytes.Equal(existing, data) {
		return "", "", errors.New("artifact digest collision or storage corruption")
	}
	return key, hash, nil
}
func (s *FileStore) Get(ctx context.Context, key string) ([]byte, error) {
	if !objectKey.MatchString(key) {
		return nil, errors.New("invalid object key")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	data, e := os.ReadFile(filepath.Join(s.Root, key))
	if e != nil {
		return nil, e
	}
	if digest(data)+".json" != key {
		return nil, errors.New("artifact integrity check failed")
	}
	return data, nil
}

type AzureStore struct {
	Client    *azblob.Client
	Container string
}

func NewAzureStore(endpoint, container string) (*AzureStore, error) {
	cred, e := azidentity.NewDefaultAzureCredential(nil)
	if e != nil {
		return nil, e
	}
	c, e := azblob.NewClient(endpoint, cred, nil)
	return &AzureStore{c, container}, e
}
func (s *AzureStore) Put(ctx context.Context, data []byte) (string, string, error) {
	hash := digest(data)
	key := hash + ".json"
	ct := "application/json"
	etag := azcore.ETag("*")
	_, e := s.Client.UploadBuffer(ctx, s.Container, key, data, &azblob.UploadBufferOptions{HTTPHeaders: &blob.HTTPHeaders{BlobContentType: &ct}, Metadata: map[string]*string{"sha256": &hash}, AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: &etag}}})
	var responseError *azcore.ResponseError
	if errors.As(e, &responseError) && responseError.StatusCode == 412 {
		existing, readError := s.Get(ctx, key)
		if readError != nil {
			return "", "", readError
		}
		if !bytes.Equal(existing, data) {
			return "", "", errors.New("existing Azure artifact content differs")
		}
		e = nil
	}
	return key, hash, e
}
func (s *AzureStore) Get(ctx context.Context, key string) ([]byte, error) {
	if !objectKey.MatchString(key) {
		return nil, errors.New("invalid object key")
	}
	resp, e := s.Client.DownloadStream(ctx, s.Container, key, nil)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if e != nil {
		return nil, e
	}
	if digest(data)+".json" != key {
		return nil, errors.New("artifact integrity check failed")
	}
	return data, nil
}
