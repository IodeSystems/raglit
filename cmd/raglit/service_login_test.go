package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestLocalBase(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "http://127.0.0.1:7420",
		"0.0.0.0:7420":                 "http://127.0.0.1:7420",
		":7499":                        "http://127.0.0.1:7499",
		"10.9.9.9:7420,127.0.0.1:7420": "http://127.0.0.1:7420",
	} {
		if got := localBase(in); got != want {
			t.Errorf("localBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// login-admin asks the running daemon for its token file and builds the
// /auth/admin URL with the token in the fragment.
func TestServiceLoginAdmin_Print(t *testing.T) {
	srv, _ := authTestServer(t)
	raw, err := os.ReadFile(adminTokenPath)
	if err != nil {
		t.Fatal(err)
	}
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err = serviceLoginAdmin([]string{"--url", srv.URL, "--print"})
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	want := srv.URL + "/auth/admin#token=" + strings.TrimSpace(string(raw))
	if !strings.Contains(string(out), want) {
		t.Errorf("output %q lacks %q", out, want)
	}
}
