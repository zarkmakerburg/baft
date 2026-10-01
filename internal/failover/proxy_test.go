package failover

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func reserve(t *testing.T) string { t.Helper();ln,e:=net.Listen("tcp","127.0.0.1:0");if e!=nil{t.Fatal(e)};a:=ln.Addr().String();_ = ln.Close();return a }

func TestProxyFallsBackToSecondary(t *testing.T){
	secondary,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)};defer secondary.Close()
	go func(){for{c,e:=secondary.Accept();if e!=nil{return};go func(x net.Conn){defer x.Close();_,_=io.Copy(x,x)}(c)}}()
	p:=&Proxy{Listen:reserve(t),Targets:[]Target{{Name:"direct",Address:"127.0.0.1:1"},{Name:"mesh",Address:secondary.Addr().String()}},DialTimeout:100*time.Millisecond}
	ctx,cancel:=context.WithCancel(context.Background());defer cancel()
	done:=make(chan error,1);go func(){done<-p.Run(ctx)}()
	deadline:=time.Now().Add(2*time.Second)
	for{
		c,e:=net.DialTimeout("tcp",p.Listen,50*time.Millisecond)
		if e==nil{
			payload:=bytes.Repeat([]byte("mesh-failover|"),32);_ = c.SetDeadline(time.Now().Add(time.Second))
			if _,e=c.Write(payload);e!=nil{t.Fatal(e)}
			got:=make([]byte,len(payload));if _,e=io.ReadFull(c,got);e!=nil{t.Fatal(e)};_ = c.Close()
			if !bytes.Equal(got,payload){t.Fatal("failover roundtrip mismatch")}
			break
		}
		if time.Now().After(deadline){t.Fatal(e)}
		time.Sleep(10*time.Millisecond)
	}
	if p.Failovers()==0{t.Fatal("secondary path was not recorded as failover")}
	cancel();select{case e:=<-done:if e!=nil{t.Fatal(e)};case <-time.After(time.Second):t.Fatal("proxy shutdown timeout")}
}
