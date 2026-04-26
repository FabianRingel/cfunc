// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package gateway

// Non-Unix targets (Plan 9, JS) lack O_NOFOLLOW. The remove-before-open
// dance still mitigates the symlink-overwrite path, just without
// kernel-enforced guarantees.
const syscallNoFollow = 0
