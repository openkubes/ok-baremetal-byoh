// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplaceupgrade

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
)

// index checks the pinned packages of the request against the signed index of
// the repository, independently of who wrote the request: Release.gpg must be
// a valid detached signature of Release by the pinned key (gpgv with the
// keyring that the Repository step checked), the Packages file must have the
// SHA256 and the size that Release lists, and every pinned package must be in
// Packages with exactly its name, version, architecture, file and SHA256. A
// digest that the repository has not signed is never downloaded.
func (e *Executor) index(ctx context.Context, req *infrav1.InPlaceUpgradeRequest) error {
	dir := filepath.Join(e.StateDir, "inplace-upgrade-"+req.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	release := filepath.Join(dir, "Release")
	signature := filepath.Join(dir, "Release.gpg")
	packages := filepath.Join(dir, "Packages")
	for _, f := range []string{release, signature, packages} {
		_ = os.Remove(f)
	}
	for file, path := range map[string]string{"Release": release, "Release.gpg": signature} {
		if err := e.Fetcher.Fetch(ctx, req.Packages.Repository+file, path); err != nil {
			return fmt.Errorf("downloading %s: %w", file, err)
		}
	}
	// The detached signature covers the exact bytes of Release; gpgv writes
	// nothing.
	status, err := e.Runner.Run(ctx, "gpgv", "--status-fd", "1", "--keyring", e.paths().Keyring, signature, release)
	if err != nil {
		return fmt.Errorf("the signature of Release does not verify: %w", err)
	}
	if err := checkSignatureStatus(status, req.Packages.RepositoryKeyFingerprint); err != nil {
		return err
	}
	releaseData, err := os.ReadFile(release)
	if err != nil {
		return err
	}
	sum, size, err := releaseEntry(releaseData, "Packages")
	if err != nil {
		return err
	}
	if err := e.Fetcher.Fetch(ctx, req.Packages.Repository+"Packages", packages); err != nil {
		return fmt.Errorf("downloading Packages: %w", err)
	}
	info, err := os.Stat(packages)
	if err != nil {
		return err
	}
	got, err := fileSHA256(packages)
	if err != nil {
		return err
	}
	if got != sum || info.Size() != size {
		return fmt.Errorf("Packages has the SHA256 %s and %d bytes, the signed Release lists %s and %d bytes", got, info.Size(), sum, size)
	}
	packagesData, err := os.ReadFile(packages)
	if err != nil {
		return err
	}
	stanzas := parseStanzas(packagesData)
	for i := range req.Packages.Debs {
		deb := &req.Packages.Debs[i]
		file := fmt.Sprintf("%s/%s_%s_%s.deb", e.arch(), deb.Name, deb.Version, e.arch())
		found := false
		for _, st := range stanzas {
			if st["Package"] != deb.Name || st["Version"] != deb.Version || st["Architecture"] != e.arch() {
				continue
			}
			if found {
				return fmt.Errorf("the signed index lists %s %s %s twice", deb.Name, deb.Version, e.arch())
			}
			found = true
			if st["SHA256"] != deb.SHA256 || st["Filename"] != file {
				return fmt.Errorf("the signed index lists %s %s as %s with SHA256 %s, the request pins %s", deb.Name, deb.Version, st["Filename"], st["SHA256"], deb.SHA256)
			}
		}
		if !found {
			return fmt.Errorf("the signed index does not list %s %s for %s", deb.Name, deb.Version, e.arch())
		}
	}
	return nil
}

// checkSignatureStatus requires a good, valid signature whose primary key is
// the pinned one, from the --status-fd output of gpgv. A VALIDSIG line ends
// with the fingerprint of the primary key.
func checkSignatureStatus(status []byte, fingerprint string) error {
	good, valid := false, false
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "[GNUPG:]" {
			continue
		}
		switch fields[1] {
		case "GOODSIG":
			good = true
		case "VALIDSIG":
			if fields[len(fields)-1] == fingerprint {
				valid = true
			}
		case "BADSIG", "ERRSIG", "EXPKEYSIG", "REVKEYSIG", "EXPSIG", "NO_PUBKEY":
			return fmt.Errorf("gpgv reports %s for Release", fields[1])
		}
	}
	if !good || !valid {
		return fmt.Errorf("Release is not signed by the key %s", fingerprint)
	}
	return nil
}

// releaseEntry returns the SHA256 and the size of a file in the SHA256 list
// of a Release file.
func releaseEntry(release []byte, name string) (string, int64, error) {
	inSHA256 := false
	sc := bufio.NewScanner(bytes.NewReader(release))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, " ") {
			inSHA256 = strings.TrimSpace(line) == "SHA256:"
			continue
		}
		fields := strings.Fields(line)
		if inSHA256 && len(fields) == 3 && fields[2] == name {
			size, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil || len(fields[0]) != 64 {
				return "", 0, fmt.Errorf("malformed SHA256 entry of %s in Release", name)
			}
			return fields[0], size, nil
		}
	}
	return "", 0, fmt.Errorf("the signed Release has no SHA256 of %s", name)
}

// parseStanzas parses the stanzas of a Packages file, without continuation
// lines (the fields that the check reads have none).
func parseStanzas(data []byte) []map[string]string {
	var stanzas []map[string]string
	current := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(current) > 0 {
				stanzas = append(stanzas, current)
			}
			current = map[string]string{}
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			current[key] = strings.TrimSpace(value)
		}
	}
	if len(current) > 0 {
		stanzas = append(stanzas, current)
	}
	return stanzas
}
