// SPDX-License-Identifier: Apache-2.0

// Example cfunc handler in Go. Build:
//
//	go build -o /tmp/example ./templates/go/example
//
// Then run the gateway with -binary=/tmp/example.
package main

import (
	"context"
	"encoding/json"
	"log"

	cfunc "github.com/fabianringel/cfunc/sdks/go"
)

func main() {
	if err := cfunc.Start(handle); err != nil {
		log.Fatal(err)
	}
}

func handle(ctx context.Context, e cfunc.Event) (cfunc.Response, error) {
	body, _ := json.Marshal(map[string]string{
		"hello":  "world",
		"method": e.Method,
		"path":   e.Path,
	})
	return cfunc.Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, nil
}
