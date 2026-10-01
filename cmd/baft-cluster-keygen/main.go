package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zarkmakerburg/baft/internal/clustersync"
)

func write(path, value string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path),0700); err != nil { return err }
	return os.WriteFile(path,[]byte(value+"\n"),mode)
}

func main() {
	out := flag.String("out-dir",".","directory for cluster key files")
	flag.Parse()
	if flag.NArg()!=0 { fmt.Fprintln(os.Stderr,"usage: baft-cluster-keygen [--out-dir <dir>]"); os.Exit(2) }

	wp,wpub,err := clustersync.GenerateWorkerKeyPair()
	if err != nil { fmt.Fprintln(os.Stderr,err); os.Exit(1) }
	spub,spriv,err := clustersync.GenerateSigningKeyPair()
	if err != nil { fmt.Fprintln(os.Stderr,err); os.Exit(1) }

	files := []struct{name,value string; mode os.FileMode}{
		{"worker.x25519.key",clustersync.EncodeWorkerPrivate(wp),0600},
		{"worker.x25519.pub",clustersync.EncodeWorkerPublic(wpub),0644},
		{"master.ed25519.key",clustersync.EncodeSigningPrivate(spriv),0600},
		{"master.ed25519.pub",clustersync.EncodeSigningPublic(spub),0644},
	}
	for _,f := range files {
		p:=filepath.Join(*out,f.name)
		if err:=write(p,f.value,f.mode);err!=nil{fmt.Fprintln(os.Stderr,p,err);os.Exit(1)}
		fmt.Println(p)
	}
}
