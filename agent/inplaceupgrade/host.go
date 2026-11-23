// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplaceupgrade

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// maxErrorOutput bounds the output of a failed command in its error.
const maxErrorOutput = 2048

// ExecRunner runs commands on the host, without a shell.
type ExecRunner struct{}

// Run implements Runner. The error of a failed command carries the end of its
// standard error.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		tail := stderr.String()
		if len(tail) > maxErrorOutput {
			tail = tail[len(tail)-maxErrorOutput:]
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(tail))
	}
	return stdout.Bytes(), nil
}

// HTTPFetcher downloads over HTTPS only.
type HTTPFetcher struct {
	Client *http.Client
}

// Fetch implements Fetcher.
func (f HTTPFetcher) Fetch(ctx context.Context, url, path string) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("refusing to download %s without TLS", url)
	}
	client := f.Client
	if client == nil {
		// pkgs.k8s.io redirects to its CDN: follow HTTPS redirects only.
		client = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) > 5 {
				return fmt.Errorf("refusing the redirect to %s", req.URL)
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
