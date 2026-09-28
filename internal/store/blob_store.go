package store

import (
	"context"
	"io"
	"time"
)

// A BlobStore keeps blobs, bytes under a key with a content type, each
// namespace's apart from the others'. Blobs have no versions: the last write
// wins, as in object stores.
type BlobStore interface {
	// Put writes the blob under key from r, replacing what was there.
	Put(ctx context.Context, ns, key string, r io.Reader, contentType string) error
	// Get opens the blob for reading; the caller closes it. It returns
	// ErrNotFound if there's no such key.
	Get(ctx context.Context, ns, key string) (io.ReadCloser, BlobInfo, error)
	// Stat describes the blob without reading it; ErrNotFound if none.
	Stat(ctx context.Context, ns, key string) (BlobInfo, error)
	// Delete removes the blob, if it's there.
	Delete(ctx context.Context, ns, key string) error
	// List describes the blobs whose key starts with prefix, by key.
	List(ctx context.Context, ns, prefix string, limit int) ([]BlobInfo, error)
}

// BlobInfo describes a blob.
type BlobInfo struct {
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type,omitempty"`
	Modified    time.Time `json:"modified"`
}
