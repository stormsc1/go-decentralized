// Package file is the file blob store driver: blobs as files in a directory,
// through the Go CDK (gocloud.dev/blob), which S3 and other object stores
// will share. It registers itself as "file". Without a data directory, blobs
// stay in memory.
package file

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gocloud.dev/blob"
	"gocloud.dev/blob/fileblob"
	"gocloud.dev/blob/memblob"
	"gocloud.dev/gcerrors"

	"go-decentralized/internal/store"
)

func init() {
	store.Register("file", func(cfg store.Config, dataDir string) (store.Store, error) {
		return Open(resolve(cfg.Options.String("path"), dataDir))
	})
}

// resolve resolves a configured directory: relative ones under dataDir, and
// to memory without a dataDir.
func resolve(path, dataDir string) string {
	if dataDir == "" {
		return ""
	}
	if path == "" {
		path = "blobs"
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dataDir, path)
}

// A Bucket is an open blob store: files under a directory, or in memory if
// the directory is "".
type Bucket struct {
	bucket *blob.Bucket
}

// Open opens the blob store in dir, creating it if needed; an empty dir
// keeps blobs in memory.
func Open(dir string) (*Bucket, error) {
	if dir == "" {
		return &Bucket{memblob.OpenBucket(nil)}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Written in place, not through the system's temp dir: that may be
	// another filesystem, e.g. in a container, where a rename can't cross.
	b, err := fileblob.OpenBucket(dir, &fileblob.Options{CreateDir: true, NoTempDir: true})
	if err != nil {
		return nil, err
	}
	return &Bucket{b}, nil
}

func (b *Bucket) Close() error { return b.bucket.Close() }

// Blobs returns the bucket as a store of blobs.
func (b *Bucket) Blobs() store.BlobStore { return blobStore{b.bucket} }

type blobStore struct{ bucket *blob.Bucket }

// path is where a namespace's key lives in the bucket.
func path(ns, key string) string { return ns + "/" + key }

func translate(err error) error {
	if gcerrors.Code(err) == gcerrors.NotFound {
		return store.ErrNotFound
	}
	return err
}

func (s blobStore) Put(ctx context.Context, ns, key string, r io.Reader, contentType string) error {
	if key == "" || strings.Contains(key, "..") {
		return errors.New("invalid blob key")
	}
	w, err := s.bucket.NewWriter(ctx, path(ns, key), &blob.WriterOptions{ContentType: contentType})
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func (s blobStore) Get(ctx context.Context, ns, key string) (io.ReadCloser, store.BlobInfo, error) {
	r, err := s.bucket.NewReader(ctx, path(ns, key), nil)
	if err != nil {
		return nil, store.BlobInfo{}, translate(err)
	}
	return r, store.BlobInfo{Key: key, Size: r.Size(), ContentType: r.ContentType(), Modified: r.ModTime()}, nil
}

func (s blobStore) Stat(ctx context.Context, ns, key string) (store.BlobInfo, error) {
	a, err := s.bucket.Attributes(ctx, path(ns, key))
	if err != nil {
		return store.BlobInfo{}, translate(err)
	}
	return store.BlobInfo{Key: key, Size: a.Size, ContentType: a.ContentType, Modified: a.ModTime}, nil
}

func (s blobStore) Delete(ctx context.Context, ns, key string) error {
	err := s.bucket.Delete(ctx, path(ns, key))
	if gcerrors.Code(err) == gcerrors.NotFound {
		return nil
	}
	return err
}

func (s blobStore) List(ctx context.Context, ns, prefix string, limit int) ([]store.BlobInfo, error) {
	iter := s.bucket.List(&blob.ListOptions{Prefix: path(ns, prefix)})
	var out []store.BlobInfo
	for len(out) < limit {
		obj, err := iter.Next(ctx)
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if obj.IsDir {
			continue
		}
		out = append(out, store.BlobInfo{Key: strings.TrimPrefix(obj.Key, ns+"/"), Size: obj.Size, Modified: obj.ModTime})
	}
	return out, nil
}
