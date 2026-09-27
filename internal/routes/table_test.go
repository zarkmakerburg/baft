package routes

import "testing"

func TestResolveUsesFixedAllowlistedTarget(t *testing.T){tbl,err:=New([]Route{{ID:"main",Target:"127.0.0.1:2443",AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}}}});if err!=nil{t.Fatal(err)};got,err:=tbl.Resolve("urn:baft:node:ir-01","main");if err!=nil||got!="127.0.0.1:2443"{t.Fatalf("got=%q err=%v",got,err)};if _,err:=tbl.Resolve("urn:baft:node:other","main");err==nil{t.Fatal("expected route denial")};if _,err:=tbl.Resolve("urn:baft:node:ir-01","missing");err==nil{t.Fatal("expected route not found")}}
