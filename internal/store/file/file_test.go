package file

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"go-decentralized/internal/store"
)

func TestBlobs(t *testing.T) {
	for _, dir := range []string{"", t.TempDir()} {
		t.Run(map[bool]string{true: "memory", false: "files"}[dir == ""], func(t *testing.T) {
			b, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			ctx := context.Background()
			blobs := b.Blobs()
			if _, _, err := blobs.Get(ctx, "a/x", "none"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("got a blob that isn't there: err = %v", err)
			}
			if err := blobs.Put(ctx, "a/x", "pics/one", strings.NewReader("PNG..."), "image/png"); err != nil {
				t.Fatal(err)
			}
			if err := blobs.Put(ctx, "a/x", "pics/two", strings.NewReader("more"), "image/png"); err != nil {
				t.Fatal(err)
			}
			if err := blobs.Put(ctx, "b/y", "pics/one", strings.NewReader("other namespace"), ""); err != nil {
				t.Fatal(err)
			}
			r, info, err := blobs.Get(ctx, "a/x", "pics/one")
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(r)
			r.Close()
			if string(data) != "PNG..." || info.Size != 6 || info.ContentType != "image/png" || info.Key != "pics/one" {
				t.Fatalf("got %q, %+v", data, info)
			}
			if info, err := blobs.Stat(ctx, "a/x", "pics/two"); err != nil || info.Size != 4 {
				t.Fatalf("stat = %+v, %v", info, err)
			}
			list, err := blobs.List(ctx, "a/x", "pics/", 10)
			if err != nil || len(list) != 2 || list[0].Key != "pics/one" || list[1].Key != "pics/two" {
				t.Fatalf("list = %+v, %v", list, err)
			}
			if list, _ := blobs.List(ctx, "a/x", "", 1); len(list) != 1 {
				t.Fatalf("list of 1 = %+v", list)
			}
			// Replacing, deleting; deleting what isn't there is fine.
			if err := blobs.Put(ctx, "a/x", "pics/one", strings.NewReader("JPEG"), "image/jpeg"); err != nil {
				t.Fatal(err)
			}
			if info, err := blobs.Stat(ctx, "a/x", "pics/one"); err != nil || info.ContentType != "image/jpeg" || info.Size != 4 {
				t.Fatalf("replaced = %+v, %v", info, err)
			}
			if err := blobs.Delete(ctx, "a/x", "pics/one"); err != nil {
				t.Fatal(err)
			}
			if err := blobs.Delete(ctx, "a/x", "pics/one"); err != nil {
				t.Fatalf("deleting twice: %v", err)
			}
			if _, err := blobs.Stat(ctx, "a/x", "pics/one"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("stat after delete: err = %v", err)
			}
			if _, err := blobs.Stat(ctx, "b/y", "pics/one"); err != nil {
				t.Fatalf("the other namespace's blob went too: %v", err)
			}
			if err := blobs.Put(ctx, "a/x", "../escape", strings.NewReader("x"), ""); err == nil {
				t.Fatal("put a key that escapes the namespace")
			}
		})
	}
}
