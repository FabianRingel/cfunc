package providers

import (
	"github.com/libdns/digitalocean"

	"github.com/fabianringel/cfunc/internal/tls"
)

func init() {
	tls.Register("digitalocean", func(env tls.Env) (tls.DNSProvider, error) {
		token, err := env.Required("DO_AUTH_TOKEN")
		if err != nil {
			return nil, err
		}
		return &digitalocean.Provider{APIToken: token}, nil
	})
}
