package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
}

func TestEmptyTokenIsPassthrough(t *testing.T) {
	srv := httptest.NewServer(TokenAuth("", okHandler()))
	defer srv.Close()
	resp, _ := http.Get(srv.URL)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestRejectsMissingToken(t *testing.T) {
	srv := httptest.NewServer(TokenAuth("s3cret", okHandler()))
	defer srv.Close()
	resp, _ := http.Get(srv.URL)
	if resp.StatusCode != 401 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("missing challenge: %q", resp.Header.Get("WWW-Authenticate"))
	}
}

func TestAcceptsBearerHeader(t *testing.T) {
	srv := httptest.NewServer(TokenAuth("s3cret", okHandler()))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestAcceptsQueryToken(t *testing.T) {
	srv := httptest.NewServer(TokenAuth("s3cret", okHandler()))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "?token=s3cret")
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestRejectsWrongToken(t *testing.T) {
	srv := httptest.NewServer(TokenAuth("s3cret", okHandler()))
	defer srv.Close()

	cases := []func() *http.Request{
		func() *http.Request {
			r, _ := http.NewRequest("GET", srv.URL+"?token=nope", nil)
			return r
		},
		func() *http.Request {
			r, _ := http.NewRequest("GET", srv.URL, nil)
			r.Header.Set("Authorization", "Bearer nope")
			return r
		},
		func() *http.Request {
			r, _ := http.NewRequest("GET", srv.URL, nil)
			r.Header.Set("Authorization", "Basic czNjcmV0Og==")
			return r
		},
	}
	for i, mk := range cases {
		resp, _ := http.DefaultClient.Do(mk())
		if resp.StatusCode != 401 {
			t.Errorf("case %d: status=%d", i, resp.StatusCode)
		}
	}
}

func TestLoadTokenPriority(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tok")

	// File wins
	os.WriteFile(file, []byte("from-file\n"), 0o600)
	os.Setenv("CFUNC_T", "from-env")
	defer os.Unsetenv("CFUNC_T")
	got, err := LoadToken(file, "CFUNC_T", "from-literal")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-file" {
		t.Fatalf("got %q", got)
	}

	// File missing -> env wins
	got, _ = LoadToken("", "CFUNC_T", "from-literal")
	if got != "from-env" {
		t.Fatalf("got %q", got)
	}

	// Nothing set -> literal
	os.Unsetenv("CFUNC_T")
	got, _ = LoadToken("", "CFUNC_T", "from-literal")
	if got != "from-literal" {
		t.Fatalf("got %q", got)
	}

	// All empty -> empty
	got, _ = LoadToken("", "", "")
	if got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestLoadTokenFileMissingIsError(t *testing.T) {
	if _, err := LoadToken("/no/such/file", "", ""); err == nil {
		t.Fatal("expected error")
	}
}
