// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"github.com/libdns/cloudflare"

	"github.com/fabianringel/cfunc/internal/tls"
)

func init() {
	tls.Register("cloudflare", func(env tls.Env) (tls.DNSProvider, error) {
		// Token must allow Zone:Read + DNS:Edit on the target zone.
		token, err := env.Required("CF_API_TOKEN")
		if err != nil {
			return nil, err
		}
		return &cloudflare.Provider{APIToken: token}, nil
	})
}
