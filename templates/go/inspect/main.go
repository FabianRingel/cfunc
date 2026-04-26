// SPDX-License-Identifier: Apache-2.0

// inspect is a test/diagnostic handler that returns inode + content of a
// path it stats inside the container. Used by the layer-sharing test to
// prove that two containers see the same host file via a shared bind-mount.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"syscall"

	cfunc "github.com/fabianringel/cfunc/sdks/go"
)

func main() {
	if err := cfunc.Start(handle); err != nil {
		log.Fatal(err)
	}
}

func handle(ctx context.Context, e cfunc.Event) (cfunc.Response, error) {
	target := e.Headers["X-Path"]
	if target == "" {
		target = "/opt/layers/shared/data"
	}
	st, err := os.Stat(target)
	if err != nil {
		return errResp(err.Error())
	}
	sys := st.Sys().(*syscall.Stat_t)
	content, err := os.ReadFile(target)
	if err != nil {
		return errResp(err.Error())
	}
	body, _ := json.Marshal(map[string]any{
		"path":    target,
		"inode":   sys.Ino,
		"device":  sys.Dev,
		"size":    st.Size(),
		"content": string(content),
	})
	return cfunc.Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, nil
}

func errResp(msg string) (cfunc.Response, error) {
	body, _ := json.Marshal(map[string]string{"error": msg})
	return cfunc.Response{Status: 500, Body: body}, nil
}
