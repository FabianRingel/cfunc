// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fabianringel/cfunc/internal/state"
)

// openStore opens a PostgresStore against dsn with a 10s timeout.
// All tenancy commands share this entry; the caller must Close.
func openStore(dsn string) *state.PostgresStore {
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "--dsn is required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := state.OpenPostgres(ctx, dsn)
	if err != nil {
		exit(err)
	}
	return s
}

// --- project ---------------------------------------------------------

func projectCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "expected: project create|list|delete")
		os.Exit(2)
	}
	switch args[0] {
	case "create":
		projectCreate(args[1:])
	case "list":
		projectList(args[1:])
	case "delete":
		projectDelete(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown project subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func projectCreate(args []string) {
	fs := flag.NewFlagSet("project create", flag.ExitOnError)
	dsn := fs.String("dsn", "", "Postgres DSN")
	name := fs.String("name", "", "project name (slug)")
	desc := fs.String("description", "", "human description")
	fs.Parse(args)
	if *name == "" {
		fmt.Fprintln(os.Stderr, "--name is required")
		os.Exit(2)
	}
	s := openStore(*dsn)
	defer s.Close()
	ctx := context.Background()
	if err := s.CreateProject(ctx, state.Project{Name: *name, Description: *desc}); err != nil {
		exit(err)
	}
	_ = s.AppendAudit(ctx, state.AuditEntry{
		Actor: "cli", Action: "project.create", Target: *name,
	})
	fmt.Printf("ok — project %q created\n", *name)
}

func projectList(args []string) {
	fs := flag.NewFlagSet("project list", flag.ExitOnError)
	dsn := fs.String("dsn", "", "Postgres DSN")
	fs.Parse(args)
	s := openStore(*dsn)
	defer s.Close()
	projects, err := s.ListProjects(context.Background())
	if err != nil {
		exit(err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDESCRIPTION\tCREATED")
	for _, p := range projects {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, p.Description, p.CreatedAt.Format(time.RFC3339))
	}
	tw.Flush()
}

func projectDelete(args []string) {
	fs := flag.NewFlagSet("project delete", flag.ExitOnError)
	dsn := fs.String("dsn", "", "Postgres DSN")
	name := fs.String("name", "", "project name")
	fs.Parse(args)
	if *name == "" {
		fmt.Fprintln(os.Stderr, "--name is required")
		os.Exit(2)
	}
	s := openStore(*dsn)
	defer s.Close()
	if err := s.DeleteProject(context.Background(), *name); err != nil {
		exit(err)
	}
	_ = s.AppendAudit(context.Background(), state.AuditEntry{
		Actor: "cli", Action: "project.delete", Target: *name,
	})
	fmt.Printf("ok — project %q deleted\n", *name)
}

// --- key -------------------------------------------------------------

func keyCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "expected: key create|list|revoke")
		os.Exit(2)
	}
	switch args[0] {
	case "create":
		keyCreate(args[1:])
	case "list":
		keyList(args[1:])
	case "revoke":
		keyRevoke(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown key subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func keyCreate(args []string) {
	fs := flag.NewFlagSet("key create", flag.ExitOnError)
	dsn := fs.String("dsn", "", "Postgres DSN")
	project := fs.String("project", "", "owning project")
	desc := fs.String("description", "", "description")
	scopes := fs.String("scopes", "deploy,invoke", "comma-separated scopes")
	fs.Parse(args)
	if *project == "" {
		fmt.Fprintln(os.Stderr, "--project is required")
		os.Exit(2)
	}

	// Generate ID + plaintext token.
	idBytes := make([]byte, 6)
	if _, err := rand.Read(idBytes); err != nil {
		exit(err)
	}
	id := "ck_" + hex.EncodeToString(idBytes)
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		exit(err)
	}
	plain := id + "." + hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(plain))

	scopeList := []string{}
	for _, s := range strings.Split(*scopes, ",") {
		if t := strings.TrimSpace(s); t != "" {
			scopeList = append(scopeList, t)
		}
	}

	s := openStore(*dsn)
	defer s.Close()
	ctx := context.Background()
	err := s.CreateAPIKey(ctx, state.APIKey{
		ID: id, Project: *project, Description: *desc,
		TokenSHA256: hash[:], Scopes: scopeList,
	})
	if err != nil {
		exit(err)
	}
	_ = s.AppendAudit(ctx, state.AuditEntry{
		Project: *project, Actor: "cli", Action: "key.create", Target: id,
	})
	fmt.Printf("id:    %s\n", id)
	fmt.Printf("token: %s\n", plain)
	fmt.Println("(plaintext token shown once — store it now)")
}

func keyList(args []string) {
	fs := flag.NewFlagSet("key list", flag.ExitOnError)
	dsn := fs.String("dsn", "", "Postgres DSN")
	project := fs.String("project", "", "project")
	fs.Parse(args)
	if *project == "" {
		fmt.Fprintln(os.Stderr, "--project is required")
		os.Exit(2)
	}
	s := openStore(*dsn)
	defer s.Close()
	keys, err := s.ListAPIKeys(context.Background(), *project)
	if err != nil {
		exit(err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tDESCRIPTION\tSCOPES\tCREATED\tLAST_USED")
	for _, k := range keys {
		last := "-"
		if k.LastUsedAt != nil {
			last = k.LastUsedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			k.ID, k.Description, strings.Join(k.Scopes, ","),
			k.CreatedAt.Format(time.RFC3339), last)
	}
	tw.Flush()
}

func keyRevoke(args []string) {
	fs := flag.NewFlagSet("key revoke", flag.ExitOnError)
	dsn := fs.String("dsn", "", "Postgres DSN")
	id := fs.String("id", "", "key id")
	fs.Parse(args)
	if *id == "" {
		fmt.Fprintln(os.Stderr, "--id is required")
		os.Exit(2)
	}
	s := openStore(*dsn)
	defer s.Close()
	ctx := context.Background()
	if err := s.DeleteAPIKey(ctx, *id); err != nil {
		exit(err)
	}
	_ = s.AppendAudit(ctx, state.AuditEntry{
		Actor: "cli", Action: "key.revoke", Target: *id,
	})
	fmt.Printf("ok — key %q revoked\n", *id)
}

// --- quota -----------------------------------------------------------

func quotaCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "expected: quota set|list")
		os.Exit(2)
	}
	switch args[0] {
	case "set":
		quotaSet(args[1:])
	case "list":
		quotaList(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown quota subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func quotaSet(args []string) {
	fs := flag.NewFlagSet("quota set", flag.ExitOnError)
	dsn := fs.String("dsn", "", "Postgres DSN")
	project := fs.String("project", "", "project")
	kind := fs.String("kind", "requests_per_min", "quota kind (requests_per_min|ram_mb|layer_bytes)")
	value := fs.Int64("value", 0, "limit value (0 = unlimited)")
	fs.Parse(args)
	if *project == "" {
		fmt.Fprintln(os.Stderr, "--project is required")
		os.Exit(2)
	}
	s := openStore(*dsn)
	defer s.Close()
	ctx := context.Background()
	if err := s.SetQuota(ctx, *project, *kind, *value); err != nil {
		exit(err)
	}
	_ = s.AppendAudit(ctx, state.AuditEntry{
		Project: *project, Actor: "cli", Action: "quota.set",
		Target: *kind, Metadata: map[string]string{"value": fmt.Sprint(*value)},
	})
	fmt.Printf("ok — quota %s/%s = %d\n", *project, *kind, *value)
}

func quotaList(args []string) {
	fs := flag.NewFlagSet("quota list", flag.ExitOnError)
	dsn := fs.String("dsn", "", "Postgres DSN")
	project := fs.String("project", "", "project")
	fs.Parse(args)
	if *project == "" {
		fmt.Fprintln(os.Stderr, "--project is required")
		os.Exit(2)
	}
	s := openStore(*dsn)
	defer s.Close()
	quotas, err := s.ListQuotas(context.Background(), *project)
	if err != nil {
		exit(err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tVALUE\tUPDATED")
	for _, q := range quotas {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", q.Kind, q.Value, q.UpdatedAt.Format(time.RFC3339))
	}
	tw.Flush()
}

// --- audit -----------------------------------------------------------

func auditCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "expected: audit tail")
		os.Exit(2)
	}
	switch args[0] {
	case "tail":
		auditTail(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown audit subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func auditTail(args []string) {
	fs := flag.NewFlagSet("audit tail", flag.ExitOnError)
	dsn := fs.String("dsn", "", "Postgres DSN")
	project := fs.String("project", "", "project (empty = cluster-level events only)")
	limit := fs.Int("limit", 50, "max entries")
	fs.Parse(args)
	s := openStore(*dsn)
	defer s.Close()
	entries, err := s.ListAudit(context.Background(), *project, time.Time{}, *limit)
	if err != nil {
		exit(err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TS\tPROJECT\tACTOR\tACTION\tTARGET")
	for _, e := range entries {
		proj := e.Project
		if proj == "" {
			proj = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			e.TS.Format(time.RFC3339), proj, e.Actor, e.Action, e.Target)
	}
	tw.Flush()
}
