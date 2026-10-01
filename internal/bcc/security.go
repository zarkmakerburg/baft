package bcc

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SecurityConfig struct {
	RequestsPerWindow int
	Window            time.Duration
	MaxAuthFailures   int
	AuthFailureWindow time.Duration
	BlockDuration     time.Duration
}

type clientState struct {
	windowStart time.Time
	requests int
	failureStart time.Time
	failures int
	blockedUntil time.Time
	lastSeen time.Time
}

type IPGuard struct {
	mu sync.Mutex
	cfg SecurityConfig
	clients map[string]*clientState
	lastSweep time.Time
}

func newIPGuard(cfg SecurityConfig)*IPGuard{
	if cfg.RequestsPerWindow<=0{cfg.RequestsPerWindow=120}
	if cfg.Window<=0{cfg.Window=time.Minute}
	if cfg.MaxAuthFailures<=0{cfg.MaxAuthFailures=5}
	if cfg.AuthFailureWindow<=0{cfg.AuthFailureWindow=time.Minute}
	if cfg.BlockDuration<=0{cfg.BlockDuration=5*time.Minute}
	return &IPGuard{cfg:cfg,clients:map[string]*clientState{}}
}

func remoteIP(r *http.Request) string {
	host,_,err:=net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err==nil&&host!=""{return host}
	if ip:=net.ParseIP(strings.TrimSpace(r.RemoteAddr));ip!=nil{return ip.String()}
	return "unknown"
}

func parseTrustedProxies(values []string)(map[string]struct{},error){
	out:=map[string]struct{}{}
	for _,v:=range values{
		v=strings.TrimSpace(v);if v==""{continue}
		ip:=net.ParseIP(v)
		if ip==nil{return nil,errors.New("trusted proxy entries must be IP literals")}
		out[ip.String()]=struct{}{}
	}
	return out,nil
}

func clientIP(r *http.Request,trusted map[string]struct{}) string {
	direct:=remoteIP(r)
	if _,ok:=trusted[direct];!ok{return direct}
	xff:=strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff==""{return direct}
	first:=strings.TrimSpace(strings.Split(xff,",")[0])
	ip:=net.ParseIP(first)
	if ip==nil{return direct}
	return ip.String()
}

// sweepLocked drops clients whose rate window, failure window and block have
// all expired. Such an entry carries no state, and without this the map grows
// by one entry per client IP for the life of the process.
func (g *IPGuard) sweepLocked(now time.Time){
	if now.Sub(g.lastSweep)<g.cfg.Window{return}
	g.lastSweep=now
	for ip,st:=range g.clients{
		if now.Before(st.blockedUntil){continue}
		if !st.windowStart.IsZero()&&now.Sub(st.windowStart)<g.cfg.Window{continue}
		if !st.failureStart.IsZero()&&now.Sub(st.failureStart)<g.cfg.AuthFailureWindow{continue}
		delete(g.clients,ip)
	}
}

func (g *IPGuard) Allow(ip string,now time.Time)(bool,time.Duration){
	g.mu.Lock();defer g.mu.Unlock()
	g.sweepLocked(now)
	st:=g.clients[ip]
	if st==nil{st=&clientState{};g.clients[ip]=st}
	st.lastSeen=now
	if now.Before(st.blockedUntil){return false,st.blockedUntil.Sub(now)}
	if st.windowStart.IsZero()||now.Sub(st.windowStart)>=g.cfg.Window{
		st.windowStart=now;st.requests=0
	}
	st.requests++
	if st.requests>g.cfg.RequestsPerWindow{
		retry:=g.cfg.Window-now.Sub(st.windowStart)
		if retry<time.Second{retry=time.Second}
		return false,retry
	}
	return true,0
}

func (g *IPGuard) AuthFailure(ip string,now time.Time){
	g.mu.Lock();defer g.mu.Unlock()
	st:=g.clients[ip]
	if st==nil{st=&clientState{};g.clients[ip]=st}
	if st.failureStart.IsZero()||now.Sub(st.failureStart)>=g.cfg.AuthFailureWindow{
		st.failureStart=now;st.failures=0
	}
	st.failures++;st.lastSeen=now
	if st.failures>=g.cfg.MaxAuthFailures{
		st.blockedUntil=now.Add(g.cfg.BlockDuration)
		st.failures=0;st.failureStart=time.Time{}
	}
}

func (g *IPGuard) AuthSuccess(ip string){
	g.mu.Lock();defer g.mu.Unlock()
	if st:=g.clients[ip];st!=nil{st.failures=0;st.failureStart=time.Time{}}
}

func (g *IPGuard) middleware(now func()time.Time,resolveIP func(*http.Request)string,next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if !strings.HasPrefix(r.URL.Path,"/api/"){next.ServeHTTP(w,r);return}
		ok,retry:=g.Allow(resolveIP(r),now())
		if !ok{
			w.Header().Set("Retry-After",strconv.Itoa(int((retry+time.Second-1)/time.Second)))
			http.Error(w,"too many requests",http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w,r)
	})
}

func ValidateListenAddress(addr string,whitelist []string) error {
	host,_,err:=net.SplitHostPort(strings.TrimSpace(addr))
	if err!=nil{return errors.New("BCC listen must be host:port")}
	if host=="localhost"{return nil}
	ip:=net.ParseIP(host)
	if ip==nil{return errors.New("BCC listen host must be localhost or an IP literal")}
	if ip.IsLoopback(){return nil}
	for _,v:=range whitelist{
		allowed:=net.ParseIP(strings.TrimSpace(v))
		if allowed!=nil&&allowed.Equal(ip){return nil}
	}
	return errors.New("BCC non-loopback listen IP is not whitelisted")
}
