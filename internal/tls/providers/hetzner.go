package providers

import (
	"github.com/libdns/hetzner"

	"github.com/fabianringel/cfunc/internal/tls"
)

func init() {
	tls.Register("hetzner", func(env tls.Env) (tls.DNSProvider, error) {
		// Hetzner DNS API token (NOT the Cloud API token — separate
		// system at dns.hetzner.com/settings/api-token).
		token, err := env.Required("HETZNER_DNS_API_TOKEN")
		if err != nil {
			return nil, err
		}
		return &hetzner.Provider{AuthAPIToken: token}, nil
	})
}
