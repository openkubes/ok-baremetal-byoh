// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplaceupgrade

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPFetcher(t *testing.T) {
	dir := t.TempDir()
	if err := (HTTPFetcher{}).Fetch(context.Background(), "http://pkgs.k8s.io/x.deb", filepath.Join(dir, "x")); err == nil || !strings.Contains(err.Error(), "without TLS") {
		t.Fatalf("plain HTTP: %v", err)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("payload")) }))
	defer plain.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.deb":
			_, _ = w.Write([]byte("payload"))
		case "/downgrade":
			http.Redirect(w, r, plain.URL+"/ok.deb", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer tlsServer.Close()
	client := tlsServer.Client()
	path := filepath.Join(dir, "ok")
	if err := (HTTPFetcher{Client: &http.Client{Transport: client.Transport}}).Fetch(context.Background(), tlsServer.URL+"/ok.deb", path); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "payload" {
		t.Fatalf("got %q", data)
	}
	if err := (HTTPFetcher{Client: &http.Client{Transport: client.Transport}}).Fetch(context.Background(), tlsServer.URL+"/missing.deb", path); err == nil {
		t.Fatal("a 404 was accepted")
	}
}

func TestDefaultFetcherRefusesARedirectToHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("payload")) }))
	defer plain.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/ok.deb", http.StatusFound)
	}))
	defer tlsServer.Close()
	// The default client of HTTPFetcher, with the trust of the test server.
	saved := http.DefaultTransport
	http.DefaultTransport = tlsServer.Client().Transport
	defer func() { http.DefaultTransport = saved }()
	err := (HTTPFetcher{}).Fetch(context.Background(), tlsServer.URL+"/downgrade", filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), "refusing the redirect") {
		t.Fatalf("error %v", err)
	}
}
