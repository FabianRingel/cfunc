// SPDX-License-Identifier: Apache-2.0

// Package layerstore is the cluster-aware blob distribution layer.
// Builder pushes layer tarballs after a build; gateway pulls on first
// reference if the layer isn't already on local disk. Tarballs are
// content-addressed by sha256 digest, so the object key matches the
// manifest digest and integrity is verifiable on download.
//
// Two implementations exist: NoopStore (single-node default — pushes
// and gets are no-ops) and S3Store (any S3-compatible endpoint:
// Hetzner Object Storage, MinIO, AWS S3).
package layerstore

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned by Get when no object exists for the given digest.
var ErrNotFound = errors.New("layerstore: not found")

// Store is a blob backend keyed by digest (sha256:abc... or just abc...).
type Store interface {
	// Has reports whether digest exists in the store.
	Has(ctx context.Context, digest string) (bool, error)
	// Get returns a reader for the digest's content. Caller must Close.
	Get(ctx context.Context, digest string) (io.ReadCloser, error)
	// Put writes content under digest. Idempotent — re-puts must succeed.
	Put(ctx context.Context, digest string, r io.Reader, size int64) error
}

// Noop is the single-node default: pretends every digest is unavailable
// and silently accepts Puts. Used when no shared store is configured;
// operators in single-node mode keep everything on local disk via the
// existing layers.Store.
type Noop struct{}

func (Noop) Has(_ context.Context, _ string) (bool, error)                { return false, nil }
func (Noop) Get(_ context.Context, _ string) (io.ReadCloser, error)       { return nil, ErrNotFound }
func (Noop) Put(_ context.Context, _ string, _ io.Reader, _ int64) error  { return nil }

// stripPrefix accepts both bare hex and "sha256:..." forms and returns
// the bare hex used as object key.
func stripPrefix(digest string) string {
	const p = "sha256:"
	if len(digest) > len(p) && digest[:len(p)] == p {
		return digest[len(p):]
	}
	return digest
}
