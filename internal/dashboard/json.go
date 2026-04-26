// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"encoding/json"
	"io"
)

func newEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc
}
