// Command cfunc is the cfunc CLI. Subcommands cover layer management,
// function deployment, and (later) cron management.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	builderpkg "github.com/fabianringel/cfunc/internal/builder"
	"github.com/fabianringel/cfunc/internal/layers"
	"github.com/fabianringel/cfunc/internal/scheduler"
)

const usage = `cfunc - cloud function runner

Usage:
  cfunc layer add           --name N --version V --mount /opt/layers/X --from DIR [--runtime R]
  cfunc layer build-python  --name N --version V --requirements req.txt [--python python3]
  cfunc layer list
  cfunc layer show NAME[@VERSION]

  cfunc cron add  --id ID --schedule "*/5 * * * *" --function FN [--method M] [--body B]
  cfunc cron list
  cfunc cron rm   ID
  cfunc cron run  ID --gateway http://127.0.0.1:8080

Environment:
  CFUNC_STORE   cfunc state root (default: /var/lib/cfunc)
                Layers under <root>/layers, cron under <root>/cron.json
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "layer":
		layerCmd(os.Args[2:])
	case "cron":
		cronCmd(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func storeRoot() string {
	if v := os.Getenv("CFUNC_STORE"); v != "" {
		return v
	}
	return "/var/lib/cfunc"
}

func layersPath() string { return filepath.Join(storeRoot(), "layers") }
func cronPath() string   { return filepath.Join(storeRoot(), "cron.json") }

func openLayers() *layers.Store {
	s, err := layers.Open(layersPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open layer store: %v\n", err)
		os.Exit(1)
	}
	return s
}

func openCron() *scheduler.Store {
	s, err := scheduler.OpenStore(cronPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open cron store: %v\n", err)
		os.Exit(1)
	}
	return s
}

func layerCmd(args []string) {
	if len(args) == 0 {
		fmt.Print(usage)
		os.Exit(2)
	}
	switch args[0] {
	case "add":
		layerAdd(args[1:])
	case "build-python":
		layerBuildPython(args[1:])
	case "list", "ls":
		layerList()
	case "show":
		layerShow(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown layer subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func layerAdd(args []string) {
	fs := flag.NewFlagSet("layer add", flag.ExitOnError)
	name := fs.String("name", "", "layer name (required)")
	version := fs.String("version", "", "layer version (required)")
	mount := fs.String("mount", "", "absolute path inside container (required)")
	from := fs.String("from", "", "source directory (required)")
	runtime := fs.String("runtime", "any", "runtime tag")
	fs.Parse(args)
	if *name == "" || *version == "" || *mount == "" || *from == "" {
		fs.Usage()
		os.Exit(2)
	}
	abs, err := filepath.Abs(*from)
	if err != nil {
		exit(err)
	}
	m, err := openLayers().Add(*name, *version, *mount, *runtime, abs)
	if err != nil {
		exit(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
}

// layerBuildPython submits a build spec to the gateway's admin API.
// All actual work — pip install, hash verification, tarball — happens
// server-side. The CLI only sends the spec and stores the resulting
// layer locally.
func layerBuildPython(args []string) {
	fs := flag.NewFlagSet("layer build-python", flag.ExitOnError)
	name := fs.String("name", "", "layer name (required)")
	version := fs.String("version", "", "layer version (required)")
	req := fs.String("requirements", "", "path to a hash-pinned requirements.txt (required)")
	python := fs.String("python", "3.11", "python version, e.g. 3.11")
	mount := fs.String("mount", "", "mount path inside container (default: /opt/layers/<name>)")
	runtime := fs.String("runtime", "", "runtime tag (default: python-<version>)")
	indexURL := fs.String("index-url", "", "override pip index URL (must be on builder allow-list)")
	gateway := fs.String("gateway", "http://127.0.0.1:8081", "admin URL of the cfunc gateway")
	tokenFile := fs.String("token-file", "", "file with the gateway admin token")
	tokenLit := fs.String("token", "", "literal admin token (env preferred)")
	fs.Parse(args)
	if *name == "" || *version == "" || *req == "" {
		fs.Usage()
		os.Exit(2)
	}

	body, err := os.ReadFile(*req)
	if err != nil {
		exit(fmt.Errorf("read requirements: %w", err))
	}
	token := *tokenLit
	if token == "" {
		if *tokenFile != "" {
			b, err := os.ReadFile(*tokenFile)
			if err != nil {
				exit(fmt.Errorf("read token file: %w", err))
			}
			token = strings.TrimSpace(string(b))
		} else {
			token = strings.TrimSpace(os.Getenv("CFUNC_ADMIN_TOKEN"))
		}
	}

	spec := map[string]any{
		"name":       *name,
		"version":    *version,
		"runtime":    *runtime,
		"mount_path": *mount,
		"build": map[string]any{
			"type":         "python-pip",
			"python":       *python,
			"requirements": string(body),
			"index_url":    *indexURL,
		},
	}
	specJSON, _ := json.Marshal(spec)

	httpReq, _ := http.NewRequest("POST",
		strings.TrimRight(*gateway, "/")+"/_/api/layers/build",
		bytes.NewReader(specJSON))
	httpReq.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	cl := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cl.Do(httpReq)
	if err != nil {
		exit(fmt.Errorf("call gateway: %w", err))
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		exit(fmt.Errorf("build failed (HTTP %d): %s", resp.StatusCode, respBody))
	}

	// Parse result, extract the tarball locally, store as a layer.
	var result struct {
		Manifest    map[string]any `json:"manifest"`
		TarGzBase64 string         `json:"tar_gz_base64"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		exit(fmt.Errorf("parse builder response: %w", err))
	}
	tgz, err := base64.StdEncoding.DecodeString(result.TarGzBase64)
	if err != nil {
		exit(fmt.Errorf("decode tarball: %w", err))
	}

	tmp, err := os.MkdirTemp("", "cfunc-build-out-")
	if err != nil {
		exit(err)
	}
	defer os.RemoveAll(tmp)
	if err := builderpkg.Extract(tgz, tmp); err != nil {
		exit(fmt.Errorf("extract tarball: %w", err))
	}

	mountPath := *mount
	if mountPath == "" {
		mountPath = "/opt/layers/" + *name
	}
	rt := *runtime
	if rt == "" {
		rt = "python-" + *python
	}
	m, err := openLayers().Add(*name, *version, mountPath, rt, tmp)
	if err != nil {
		exit(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"manifest":          m,
		"builder_manifest":  result.Manifest,
	})
}

func layerList() {
	list, err := openLayers().List()
	if err != nil {
		exit(err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVERSION\tRUNTIME\tMOUNT\tDIGEST\tSIZE")
	for _, m := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
			m.Name, m.Version, m.Runtime, m.MountPath, shortDigest(m.Digest), m.Size)
	}
	tw.Flush()
}

func layerShow(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: cfunc layer show NAME[@VERSION]")
		os.Exit(2)
	}
	ref, err := layers.ParseRef(args[0])
	if err != nil {
		exit(err)
	}
	m, path, err := openLayers().Resolve(ref)
	if err != nil {
		exit(err)
	}
	out := struct {
		layers.Manifest
		HostPath string `json:"host_path"`
	}{Manifest: *m, HostPath: path}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func shortDigest(d string) string {
	const prefix = "sha256:"
	if len(d) > len(prefix)+12 {
		return d[:len(prefix)+12]
	}
	return d
}

func cronCmd(args []string) {
	if len(args) == 0 {
		fmt.Print(usage)
		os.Exit(2)
	}
	switch args[0] {
	case "add":
		cronAdd(args[1:])
	case "list", "ls":
		cronList()
	case "rm", "remove", "delete":
		cronRm(args[1:])
	case "run":
		cronRun(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown cron subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func cronAdd(args []string) {
	fs := flag.NewFlagSet("cron add", flag.ExitOnError)
	id := fs.String("id", "", "job id (required)")
	sched := fs.String("schedule", "", "cron expression (required)")
	fn := fs.String("function", "", "gateway function name (required)")
	method := fs.String("method", "POST", "HTTP method")
	body := fs.String("body", "", "request body")
	fs.Parse(args)
	if *id == "" || *sched == "" || *fn == "" {
		fs.Usage()
		os.Exit(2)
	}
	job := scheduler.Job{ID: *id, Schedule: *sched, Function: *fn, Method: *method, Body: *body}
	if err := openCron().Add(job); err != nil {
		exit(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(job)
}

func cronList() {
	jobs, err := openCron().List()
	if err != nil {
		exit(err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSCHEDULE\tFUNCTION\tMETHOD")
	for _, j := range jobs {
		method := j.Method
		if method == "" {
			method = "POST"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", j.ID, j.Schedule, j.Function, method)
	}
	tw.Flush()
}

func cronRm(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: cfunc cron rm ID")
		os.Exit(2)
	}
	if err := openCron().Remove(args[0]); err != nil {
		exit(err)
	}
}

func cronRun(args []string) {
	fs := flag.NewFlagSet("cron run", flag.ExitOnError)
	gateway := fs.String("gateway", "http://127.0.0.1:8080", "gateway base URL")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: cfunc cron run ID --gateway URL")
		os.Exit(2)
	}
	id := fs.Arg(0)
	tg := &scheduler.HTTPTrigger{BaseURL: *gateway}
	sch := scheduler.New(openCron(), tg, nil)
	if err := sch.FireNow(context.Background(), id); err != nil {
		exit(err)
	}
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
