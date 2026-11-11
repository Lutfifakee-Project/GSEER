// GSEER — internet-asset search engine.
// Copyright (C) 2026  The GSEER Authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

// gseer-release — the maintainer-side companion of the v0.1.0 signed
// update system (docs/UPDATE.md):
//
//	gseer-release keygen -pub OUT.pub -priv OUT.key
//	    generate the Ed25519 release keypair (the PRIVATE key never
//	    leaves the release machine and never enters the repository).
//	gseer-release sign -key PRIV -in manifest.json -out signed.json
//	    sign a manifest (Ed25519 over the canonical core bytes).
//	gseer-release verify -pub PUB -in signed.json
//	    pre-release self-check (the same verification the updater runs).
//
// This tool ships in the repository but is NOT part of the `gseer`
// binary: end-user machines never need signing capability.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Lutfifakee-Project/gseer/internal/update"
)

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gseer-release: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keygen":
		keygen(os.Args[2:])
	case "sign":
		signCmd(os.Args[2:])
	case "verify":
		verifyCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `gseer-release — signed-update release tooling (docs/UPDATE.md)

  gseer-release keygen -pub PUB -priv PRIV
  gseer-release sign  -key PRIV -in MANIFEST -out SIGNED
  gseer-release verify -pub PUB -in SIGNED
`)
	os.Exit(2)
}

func keygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	pubPath := fs.String("pub", "gseer-update.pub", "public key output (base64)")
	privPath := fs.String("priv", "gseer-update.key", "private key output (base64; KEEP OFFLINE)")
	_ = fs.Parse(args)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal("keygen: %v", err)
	}
	if err := os.WriteFile(*pubPath, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil { // #nosec G306 -- the PUBLIC key is a public artifact; the private half is written 0600 below
		fatal("write pub: %v", err)
	}
	if err := os.WriteFile(*privPath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		fatal("write priv: %v", err)
	}
	fmt.Printf("public  key → %s (embed this base64 in internal/update/keys.go)\n", *pubPath)
	fmt.Printf("private key → %s (mode 0600; keep offline; NEVER commit)\n", *privPath)
}

func loadPriv(path string) ed25519.PrivateKey {
	raw, err := os.ReadFile(path) // #nosec G304 -- the key ceremony tool reads the operator-supplied private-key path
	if err != nil {
		fatal("read key: %v", err)
	}
	key, err := base64.StdEncoding.DecodeString(string(trimSpace(raw)))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		fatal("%s is not a base64 Ed25519 private key (len %d)", path, len(key))
	}
	return ed25519.PrivateKey(key)
}

func loadPub(path string) ed25519.PublicKey {
	raw, err := os.ReadFile(path) // #nosec G304 -- the key ceremony tool reads the operator-supplied public-key path
	if err != nil {
		fatal("read pub: %v", err)
	}
	key, err := base64.StdEncoding.DecodeString(string(trimSpace(raw)))
	if err != nil || len(key) != ed25519.PublicKeySize {
		fatal("%s is not a base64 Ed25519 public key", path)
	}
	return ed25519.PublicKey(key)
}

func signCmd(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := fs.String("key", "gseer-update.key", "private key file")
	inPath := fs.String("in", "", "unsigned manifest JSON")
	outPath := fs.String("out", "", "signed manifest output")
	_ = fs.Parse(args)
	if *inPath == "" || *outPath == "" {
		fatal("sign needs -in and -out")
	}
	var m update.Manifest
	data, err := os.ReadFile(*inPath)
	if err != nil {
		fatal("read manifest: %v", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		fatal("manifest parse: %v", err)
	}
	if m.Signature != "" {
		fatal("manifest already carries a signature; sign the UNSIGNED core only")
	}
	signed, err := update.Sign(m, loadPriv(*keyPath))
	if err != nil {
		fatal("sign: %v", err)
	}
	if err := os.WriteFile(*outPath, signed, 0o644); err != nil { // #nosec G306 -- the signed manifest is a public release artifact (signature + metadata, no secrets)
		fatal("write signed manifest: %v", err)
	}
	fmt.Printf("signed → %s (version %s, %d artifacts)\n", *outPath, m.Version, len(m.Artifacts))
}

func verifyCmd(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	pubPath := fs.String("pub", "gseer-update.pub", "public key file")
	inPath := fs.String("in", "", "signed manifest")
	_ = fs.Parse(args)
	data, err := os.ReadFile(*inPath)
	if err != nil {
		fatal("read manifest: %v", err)
	}
	m, err := update.ParseManifest(data)
	if err != nil {
		fatal("%v", err)
	}
	if err := m.VerifySignature(loadPub(*pubPath)); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("signature OK (version %s, %d artifacts)\n", m.Version, len(m.Artifacts))
}

func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\n' || b[start] == '\r' || b[start] == '\t') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\n' || b[end-1] == '\r' || b[end-1] == '\t') {
		end--
	}
	return b[start:end]
}
