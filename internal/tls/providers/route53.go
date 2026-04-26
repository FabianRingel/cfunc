package providers

import (
	"github.com/libdns/route53"

	"github.com/fabianringel/cfunc/internal/tls"
)

func init() {
	tls.Register("route53", func(env tls.Env) (tls.DNSProvider, error) {
		// AWS credentials come from the standard chain (env vars, shared
		// credentials file, IAM instance profile). We only set the
		// region explicitly, defaulting to us-east-1 — Route 53 itself
		// is global, but the SDK requires a region.
		return &route53.Provider{
			Region: env.Optional("AWS_REGION", "us-east-1"),
			// MaxRetries left at default (libdns default = 5).
		}, nil
	})
}
