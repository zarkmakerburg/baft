package routes

import (
	"errors"
	"fmt"
	"net"
	"sync"
)

type Route struct { ID string; Target string; AllowedPeers map[string]struct{} }
type Table struct { mu sync.RWMutex; routes map[string]Route }
func New(defs []Route)(*Table,error){t:=&Table{routes:make(map[string]Route,len(defs))};for _,r:=range defs{if r.ID==""||len(r.ID)>64{return nil,errors.New("route id must be 1..64 bytes")};if _,exists:=t.routes[r.ID];exists{return nil,fmt.Errorf("duplicate route: %s",r.ID)};host,port,err:=net.SplitHostPort(r.Target);if err!=nil||port==""||port=="0"||net.ParseIP(host)==nil{return nil,fmt.Errorf("route %s target must be fixed IP:port",r.ID)};if len(r.AllowedPeers)==0{return nil,fmt.Errorf("route %s has empty peer allowlist",r.ID)};t.routes[r.ID]=r};return t,nil}
func (t *Table) Resolve(peerID,routeID string)(string,error){t.mu.RLock();r,ok:=t.routes[routeID];t.mu.RUnlock();if !ok{return "",errors.New("ROUTE_NOT_FOUND")};if _,ok:=r.AllowedPeers[peerID];!ok{return "",errors.New("ROUTE_DENIED")};return r.Target,nil}
