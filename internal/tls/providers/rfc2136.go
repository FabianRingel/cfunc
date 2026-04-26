// SPDX-License-Identifier: Apache-2.0

package providers

import (
	"github.com/libdns/rfc2136"

	"github.com/fabianringel/cfunc/internal/tls"
)

func init() {
	tls.Register("rfc2136", func(env tls.Env) (tls.DNSProvider, error) {
		// Self-hosted BIND/PowerDNS via dynamic update. All four are
		// required (no sensible defaults).
		server, err := env.Required("RFC2136_SERVER")
		if err != nil {
			return nil, err
		}
		keyName, err := env.Required("RFC2136_KEY_NAME")
		if err != nil {
			return nil, err
		}
		keyAlg, err := env.Required("RFC2136_KEY_ALG")
		if err != nil {
			return nil, err
		}
		key, err := env.Required("RFC2136_KEY")
		if err != nil {
			return nil, err
		}
		return &rfc2136.Provider{
			KeyName:    keyName,
			KeyAlg:     keyAlg,
			Key:        key,
			Server:     server,
		}, nil
	})
}
