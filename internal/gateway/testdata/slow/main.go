// Test handler used by TestPoolConcurrencyCap.
// Sleeps for X-Sleep-Ms (default 200) before responding.
package main

import (
	"context"
	"strconv"
	"time"

	cfunc "github.com/fabianringel/cfunc/sdks/go"
)

func handle(_ context.Context, e cfunc.Event) (cfunc.Response, error) {
	d := 200
	if v, ok := e.Headers["X-Sleep-Ms"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			d = n
		}
	}
	time.Sleep(time.Duration(d) * time.Millisecond)
	return cfunc.Response{Status: 200, Body: []byte(`"ok"`)}, nil
}

func main() { _ = cfunc.Start(handle) }
