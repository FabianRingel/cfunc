// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package gateway

// Non-Unix targets (Windows, Plan 9, JS) lack a portable O_NOFOLLOW.
// The categoric symlink rejection in writeEntry — combined with a fresh
// per-pull tmpdir created inside the cache root — keeps layer
// extraction safe on these platforms even without the kernel-enforced
// flag. Production targets are Linux/macOS, but cfunc must compile on
// all of them for tooling and CI.
const syscallNoFollow = 0
