// SPDX-License-Identifier: Apache-2.0

package gateway

import "sort"

func sortFunctions(fs []FunctionStats) {
	sort.Slice(fs, func(i, j int) bool { return fs[i].Name < fs[j].Name })
}
