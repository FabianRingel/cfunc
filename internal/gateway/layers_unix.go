// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import "syscall"

// syscallNoFollow is OR'd into OpenFile flags so pre-existing symlinks
// at the target path don't redirect writes outside the extraction root.
const syscallNoFollow = syscall.O_NOFOLLOW
