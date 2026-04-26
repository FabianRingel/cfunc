// SPDX-License-Identifier: Apache-2.0

package layerstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config bundles the connection parameters for any S3-compatible
// endpoint (Hetzner Object Storage, MinIO, AWS S3, …).
type S3Config struct {
	Endpoint  string // e.g. "fsn1.your-objectstorage.com" (no scheme)
	Bucket    string
	Region    string // optional; AWS requires it, MinIO/Hetzner ignore
	AccessKey string
	SecretKey string
	UseSSL    bool   // true for HTTPS (always for production)
	Prefix    string // optional key prefix, e.g. "cfunc/layers/"
}

// S3Store is an S3-compatible Store. It is safe for concurrent use.
type S3Store struct {
	client *minio.Client
	cfg    S3Config
}

// NewS3 constructs an S3-backed Store and verifies the bucket exists.
func NewS3(ctx context.Context, cfg S3Config) (*S3Store, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("layerstore/s3: endpoint and bucket required")
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("layerstore/s3: client: %w", err)
	}
	exists, err := cli.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("layerstore/s3: bucket check: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("layerstore/s3: bucket %q does not exist", cfg.Bucket)
	}
	return &S3Store{client: cli, cfg: cfg}, nil
}

func (s *S3Store) keyFor(digest string) string {
	return s.cfg.Prefix + stripPrefix(digest)
}

func (s *S3Store) Has(ctx context.Context, digest string) (bool, error) {
	_, err := s.client.StatObject(ctx, s.cfg.Bucket, s.keyFor(digest), minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *S3Store) Get(ctx context.Context, digest string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.cfg.Bucket, s.keyFor(digest), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// minio-go's GetObject returns lazily; trigger a stat so a missing
	// object surfaces as ErrNotFound here, not on the first Read.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return obj, nil
}

func (s *S3Store) Put(ctx context.Context, digest string, r io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, s.cfg.Bucket, s.keyFor(digest), r, size,
		minio.PutObjectOptions{
			ContentType: "application/octet-stream",
			// digest is content-addressed → object content is immutable;
			// metadata records the cfunc tag for forensic purposes
			UserMetadata: map[string]string{
				"cfunc-digest": digest,
			},
		})
	return err
}

func isNotFound(err error) bool {
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		return er.StatusCode == http.StatusNotFound || er.Code == "NoSuchKey"
	}
	return false
}
