// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplaceupgrade

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	dir := filepath.Join(e.stateDir(), "inplace-upgrade-"+req.ID)
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return err
	}
	sum, size, err := e.signedPackagesEntry(ctx, req, dir)
	if err != nil {
		return err
	}
	packages := filepath.Join(dir, "Packages")
	_ = os.Remove(packages)
	err = e.Fetcher.Fetch(ctx, req.Packages.Repository+"Packages", packages)
	if err != nil {
		return fmt.Errorf("downloading Packages: %w", err)
	}
	packagesData, err := os.ReadFile(packages)
	if err != nil {
		return err
	}
	if got := sha256Hex(packagesData); got != sum || int64(len(packagesData)) != size {
		return fmt.Errorf("the Packages file has the SHA256 %s and %d bytes, the signed Release lists %s and %d bytes", got, len(packagesData), sum, size)
	}
	stanzas := parseStanzas(packagesData)
	for i := range req.Packages.Debs {
		if err := e.checkPinned(stanzas, &req.Packages.Debs[i]); err != nil {
			return err
		}
	}
	return nil
}

// signedPackagesEntry downloads Release and Release.gpg, verifies the
// signature with the pinned key, and returns the SHA256 and the size that the
// signed Release lists for Packages.
func (e *Executor) signedPackagesEntry(ctx context.Context, req *infrav1.InPlaceUpgradeRequest, dir string) (sum string, size int64, err error) {
	release := filepath.Join(dir, releaseFile)
	signature := filepath.Join(dir, releaseFile+".gpg")
	for file, path := range map[string]string{releaseFile: release, releaseFile + ".gpg": signature} {
		_ = os.Remove(path)
		if err = e.Fetcher.Fetch(ctx, req.Packages.Repository+file, path); err != nil {
			return "", 0, fmt.Errorf("downloading %s: %w", file, err)
		}
	}
	// The detached signature covers the exact bytes of Release; gpgv writes
	// nothing.
	status, err := e.Runner.Run(ctx, "gpgv", "--status-fd", "1", "--keyring", e.paths().Keyring, signature, release)
	if err != nil {
		return "", 0, fmt.Errorf("the signature of Release does not verify: %w", err)
	}
	err = checkSignatureStatus(status, req.Packages.RepositoryKeyFingerprint)
	if err != nil {
		return "", 0, err
	}
	releaseData, err := os.ReadFile(release)
	if err != nil {
		return "", 0, err
	}
	return releaseEntry(releaseData, "Packages")
}

// checkPinned requires the pinned package in the signed Packages exactly
// once, with its file and SHA256.
func (e *Executor) checkPinned(stanzas []map[string]string, deb *infrav1.PinnedDeb) error {
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
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sha256HexLen is the length of a hexadecimal SHA256, and statusBadSig the gpgv
// status of a bad signature.
const (
	sha256HexLen = 64
	statusBadSig = "BADSIG"
)

// failedSignatureStatus are the gpgv status keywords of a signature that
// does not verify.
var failedSignatureStatus = []string{statusBadSig, "ERRSIG", "EXPKEYSIG", "REVKEYSIG", "EXPSIG", "NO_PUBKEY"}

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
		default:
			if slices.Contains(failedSignatureStatus, fields[1]) {
				return fmt.Errorf("gpgv reports %s for Release", fields[1])
			}
		}
	}
	if !good || !valid {
		return fmt.Errorf("the Release file is not signed by the key %s", fingerprint)
	}
	return nil
}

// releaseEntry returns the SHA256 and the size of a file in the SHA256 list
// of a Release file.
func releaseEntry(release []byte, name string) (sum string, size int64, err error) {
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
			size, err = strconv.ParseInt(fields[1], 10, 64)
			if err != nil || len(fields[0]) != sha256HexLen {
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
	sc.Buffer(make([]byte, 0, initialPackagesBufSize), maxPackagesLineBytes)
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
