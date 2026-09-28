package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
)

func main(){
	listen:=flag.String("listen","127.0.0.1:8080","BCC HTTP listen address")
	stateFile:=flag.String("state-file","./bcc-state.json","persistent BCC state file")
	adminTokenFile:=flag.String("admin-token-file","","file containing BCC admin bearer token")
	healthInterval:=flag.Duration("health-interval",10*time.Second,"node TCP health-check interval")
	flag.Parse()
	if flag.NArg()!=0||*adminTokenFile==""{
		fmt.Fprintln(os.Stderr,"usage: baft-bcc --admin-token-file <file> [--listen 127.0.0.1:8080] [--state-file bcc-state.json]")
		os.Exit(2)
	}
	raw,err:=os.ReadFile(*adminTokenFile)
	if err!=nil{fmt.Fprintln(os.Stderr,"admin token:",err);os.Exit(1)}
	adminToken:=strings.TrimSpace(string(raw))
	if adminToken==""{fmt.Fprintln(os.Stderr,"admin token is empty");os.Exit(1)}

	store,err:=bcc.OpenStore(*stateFile)
	if err!=nil{fmt.Fprintln(os.Stderr,"BCC state:",err);os.Exit(1)}
	app,err:=bcc.NewServer(store,adminToken)
	if err!=nil{fmt.Fprintln(os.Stderr,"BCC server:",err);os.Exit(1)}

	ctx,stop:=signal.NotifyContext(context.Background(),os.Interrupt,syscall.SIGTERM)
	defer stop()
	go app.StartHealthLoop(ctx,*healthInterval)

	srv:=&http.Server{Addr:*listen,Handler:app.Handler(),ReadHeaderTimeout:5*time.Second}
	done:=make(chan error,1)
	go func(){done<-srv.ListenAndServe()}()
	fmt.Printf("BAFT Command Center listening on http://%s\n",*listen)

	select{
	case <-ctx.Done():
		shutdownCtx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	case err:=<-done:
		if err!=nil&&err!=http.ErrServerClosed{fmt.Fprintln(os.Stderr,"BCC stopped:",err);os.Exit(1)}
	}
}
