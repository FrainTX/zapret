// Command sign creates the release signing key and signs SHA256SUMS.
//
//	go run ./tools/sign -gen KEYFILE     new key pair; prints the public key
//	go run ./tools/sign SHA256SUMS       writes SHA256SUMS.sig
//
// The private key (base64 ed25519 seed) is read from $PROSVET_SIGNING_KEY
// or from the file named by $PROSVET_SIGNING_KEY_FILE. Clients refuse any
// update whose SHA256SUMS is not signed by it.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	gen := flag.String("gen", "", "create a key pair, write the private key to this file")
	flag.Parse()
	if *gen != "" {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		check(err)
		if _, err := os.Stat(*gen); err == nil {
			check(fmt.Errorf("%s already exists — refusing to overwrite a signing key", *gen))
		}
		check(os.WriteFile(*gen, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600))
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return
	}
	if flag.NArg() != 1 {
		check(fmt.Errorf("usage: sign SHA256SUMS | sign -gen KEYFILE"))
	}
	seedB64 := os.Getenv("PROSVET_SIGNING_KEY")
	if f := os.Getenv("PROSVET_SIGNING_KEY_FILE"); seedB64 == "" && f != "" {
		b, err := os.ReadFile(f)
		check(err)
		seedB64 = string(b)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(seedB64))
	if err != nil || len(seed) != ed25519.SeedSize {
		check(fmt.Errorf("no valid signing key in PROSVET_SIGNING_KEY / PROSVET_SIGNING_KEY_FILE"))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	data, err := os.ReadFile(flag.Arg(0))
	check(err)
	sig := ed25519.Sign(priv, data)
	check(os.WriteFile(flag.Arg(0)+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644))
	fmt.Println("signed", flag.Arg(0), "with key", base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}
