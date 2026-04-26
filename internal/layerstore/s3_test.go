// SPDX-License-Identifier: Apache-2.0

package layerstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
	"testing"
)

// uniqueDigest returns a sha256-shaped string built from random bytes
// so each test run uses fresh object keys — earlier runs that crashed
// or left state don't pollute the next.
func uniqueDigest(t *testing.T) string {
	t.Helper()
	var b [32]byte
	_, _ = rand.Read(b[:])
	return "sha256:" + hex.EncodeToString(b[:])
}

// s3TestStore returns a configured S3Store or skips. Set:
//   TEST_S3_ENDPOINT=127.0.0.1:9000
//   TEST_S3_BUCKET=cfunc-test
//   TEST_S3_ACCESS=minioadmin
//   TEST_S3_SECRET=minioadmin
//   TEST_S3_SSL=false
func s3TestStore(t *testing.T) *S3Store {
	t.Helper()
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	bucket := os.Getenv("TEST_S3_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Skip("TEST_S3_ENDPOINT / TEST_S3_BUCKET not set; skipping S3 tests")
	}
	useSSL, _ := strconv.ParseBool(os.Getenv("TEST_S3_SSL"))
	s, err := NewS3(context.Background(), S3Config{
		Endpoint:  endpoint,
		Bucket:    bucket,
		AccessKey: os.Getenv("TEST_S3_ACCESS"),
		SecretKey: os.Getenv("TEST_S3_SECRET"),
		UseSSL:    useSSL,
		Prefix:    "test/",
	})
	if err != nil {
		t.Fatalf("connect S3: %v", err)
	}
	return s
}

func TestS3PutGet(t *testing.T) {
	s := s3TestStore(t)
	ctx := context.Background()
	digest := uniqueDigest(t)
	content := []byte("hello cfunc")
	if err := s.Put(ctx, digest, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(ctx, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != string(content) {
		t.Fatalf("got %q want %q", got, content)
	}
}

func TestS3HasReportsExistence(t *testing.T) {
	s := s3TestStore(t)
	ctx := context.Background()
	digest := uniqueDigest(t)

	has, _ := s.Has(ctx, digest)
	if has {
		t.Fatal("digest reports as present before put")
	}
	_ = s.Put(ctx, digest, bytes.NewReader([]byte("x")), 1)
	has, _ = s.Has(ctx, digest)
	if !has {
		t.Fatal("digest reports as missing after put")
	}
}

func TestS3GetMissing(t *testing.T) {
	s := s3TestStore(t)
	_, err := s.Get(context.Background(), uniqueDigest(t))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestS3StripsDigestPrefix(t *testing.T) {
	// Both forms must address the same object.
	s := s3TestStore(t)
	ctx := context.Background()
	full := uniqueDigest(t)
	bare := full[len("sha256:"):]
	prefixed := full
	_ = s.Put(ctx, prefixed, bytes.NewReader([]byte("x")), 1)
	has, _ := s.Has(ctx, bare)
	if !has {
		t.Fatal("bare digest should resolve to the same object")
	}
}

func TestNoopStore(t *testing.T) {
	n := Noop{}
	ctx := context.Background()
	has, err := n.Has(ctx, "anything")
	if err != nil || has {
		t.Fatalf("Noop.Has → %v %v", has, err)
	}
	if err := n.Put(ctx, "x", bytes.NewReader(nil), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Get(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Noop.Get → %v", err)
	}
}
