package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/zarkmakerburg/baft/internal/bcc"
)

func main(){
	auditPath:=flag.String("audit","","path to BCC audit JSONL")
	anchorsPath:=flag.String("anchors","","path to recorded audit anchors JSONL")
	flag.Parse()
	if *auditPath==""{fmt.Fprintln(os.Stderr,"--audit is required");os.Exit(2)}
	log,err:=bcc.OpenAuditLog(*auditPath)
	if err!=nil{fmt.Fprintln(os.Stderr,"audit verify:",err);os.Exit(1)}
	if *anchorsPath!=""{
		anchors,err:=bcc.ReadAuditAnchors(*anchorsPath)
		if err!=nil{fmt.Fprintln(os.Stderr,"anchors:",err);os.Exit(1)}
		if err:=log.VerifyAgainstAnchors(anchors);err!=nil{fmt.Fprintln(os.Stderr,"audit anchor verify:",err);os.Exit(1)}
	}else if err:=log.Verify();err!=nil{
		fmt.Fprintln(os.Stderr,"audit verify:",err);os.Exit(1)
	}
	fmt.Println("audit verification: PASS")
}
