// Research-only generator for self-made SLH-DSA-SHA2-128s vectors (pure, ctx="").
//
//	sign <dir>   : circl keys + signatures for a fixed message-length plan -> <dir>/c_<len>_<i>.{pk,msg,sig}
//	verify <dir> : circl verifies every <dir>/*.{pk,msg,sig} triple (incl. OpenSSL-made o_*), prints failures
package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudflare/circl/sign/slhdsa"
)

// plan maps message length -> number of signatures (each with a fresh key).
var plan = map[int]int{0: 8, 1: 8, 32: 8, 33: 8, 1024: 8, 65536: 2}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func sign(dir string) {
	lens := make([]int, 0, len(plan))
	for l := range plan {
		lens = append(lens, l)
	}
	sort.Ints(lens)
	for _, l := range lens {
		for i := 0; i < plan[l]; i++ {
			pub, priv, err := slhdsa.GenerateKey(rand.Reader, slhdsa.SHA2_128s)
			must(err)
			msg := make([]byte, l)
			rand.Read(msg)
			sig, err := slhdsa.SignRandomized(&priv, rand.Reader, slhdsa.NewMessage(msg), nil)
			must(err)
			pk, _ := pub.MarshalBinary()
			base := filepath.Join(dir, fmt.Sprintf("c_%05d_%d", l, i))
			must(os.WriteFile(base+".pk", pk, 0o644))
			must(os.WriteFile(base+".msg", msg, 0o644))
			must(os.WriteFile(base+".sig", sig, 0o644))
		}
	}
}

func verify(dir string) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.pk"))
	ok := 0
	for _, f := range files {
		base := strings.TrimSuffix(f, ".pk")
		pkb, _ := os.ReadFile(f)
		msg, _ := os.ReadFile(base + ".msg")
		sig, _ := os.ReadFile(base + ".sig")
		pk := slhdsa.PublicKey{ID: slhdsa.SHA2_128s}
		must(pk.UnmarshalBinary(pkb))
		if slhdsa.Verify(&pk, slhdsa.NewMessage(msg), sig, nil) {
			ok++
		} else {
			fmt.Println("FAIL circl verify:", base)
		}
	}
	fmt.Printf("circl verified %d/%d\n", ok, len(files))
}

func main() {
	switch os.Args[1] {
	case "sign":
		sign(os.Args[2])
	case "verify":
		verify(os.Args[2])
	}
}
