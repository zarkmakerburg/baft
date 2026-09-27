package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	baftmetrics "github.com/zarkmakerburg/baft/internal/metrics"
)

func (r *Runtime) startMetrics(ctx context.Context, addr string) (<-chan error, func(), error) {
	ln,err:=net.Listen("tcp",addr)
	if err!=nil{return nil,nil,fmt.Errorf("metrics listen %s: %w",addr,err)}
	srv:=&http.Server{
		Handler:baftmetrics.Handler(r.metricsSnapshot),
		ReadHeaderTimeout:5*time.Second,
		MaxHeaderBytes:8<<10,
	}
	done:=make(chan error,1)
	go func(){
		err:=srv.Serve(ln)
		if errors.Is(err,http.ErrServerClosed){err=nil}
		done<-err
	}()
	stop:=func(){
		shutdownCtx,cancel:=context.WithTimeout(context.Background(),2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = ln.Close()
	}
	go func(){<-ctx.Done();stop()}()
	return done,stop,nil
}
