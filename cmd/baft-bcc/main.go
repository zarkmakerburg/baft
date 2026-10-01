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
	telemetryStale:=flag.Duration("telemetry-stale",3*time.Minute,"telemetry stale threshold")
	handshakeErrorRate:=flag.Float64("handshake-error-rate",5.0,"handshake error alert threshold per minute")
	alertInterval:=flag.Duration("alert-interval",15*time.Second,"alert evaluation interval")
	auditAnchorInterval:=flag.Duration("audit-anchor-interval",time.Hour,"external audit anchor interval")
	backupDir:=flag.String("backup-dir","./backups","encrypted BCC backup directory")
	backupInterval:=flag.Duration("backup-interval",24*time.Hour,"encrypted backup interval")
	backupDailyRetention:=flag.Int("backup-daily-retention",7,"daily backup retention count")
	backupWeeklyRetention:=flag.Int("backup-weekly-retention",4,"weekly backup retention count")
	flag.Parse()
	if flag.NArg()!=0||*adminTokenFile==""{
		fmt.Fprintln(os.Stderr,"usage: baft-bcc --admin-token-file <file> [--listen 127.0.0.1:8080] [--state-file bcc-state.json]")
		os.Exit(2)
	}
	allowedIPs:=[]string{}
	for _,part:=range strings.Split(os.Getenv("BAFT_BCC_ALLOWED_LISTEN_IPS"),","){
		if v:=strings.TrimSpace(part);v!=""{allowedIPs=append(allowedIPs,v)}
	}
	if err:=bcc.ValidateListenAddress(*listen,allowedIPs);err!=nil{
		fmt.Fprintln(os.Stderr,"BCC listen:",err);os.Exit(2)
	}

	raw,err:=os.ReadFile(*adminTokenFile)
	if err!=nil{fmt.Fprintln(os.Stderr,"admin token:",err);os.Exit(1)}
	adminToken:=strings.TrimSpace(string(raw))
	if adminToken==""{fmt.Fprintln(os.Stderr,"admin token is empty");os.Exit(1)}

	store,err:=bcc.OpenStore(*stateFile)
	if err!=nil{fmt.Fprintln(os.Stderr,"BCC state:",err);os.Exit(1)}
	app,err:=bcc.NewServer(store,adminToken)
	if err!=nil{fmt.Fprintln(os.Stderr,"BCC server:",err);os.Exit(1)}
	trustedProxies:=[]string{}
	for _,part:=range strings.Split(os.Getenv("BAFT_BCC_TRUSTED_PROXIES"),","){
		if v:=strings.TrimSpace(part);v!=""{trustedProxies=append(trustedProxies,v)}
	}
	if err:=app.ConfigureTrustedProxies(trustedProxies);err!=nil{fmt.Fprintln(os.Stderr,"trusted proxies:",err);os.Exit(2)}
	if err:=app.ConfigureAuditAnchoring(strings.TrimSpace(os.Getenv("BAFT_BCC_AUDIT_ANCHOR_WEBHOOK_URL")),*auditAnchorInterval);err!=nil{
		fmt.Fprintln(os.Stderr,"audit anchor:",err);os.Exit(2)
	}
	if *handshakeErrorRate<=0{fmt.Fprintln(os.Stderr,"handshake-error-rate must be positive");os.Exit(2)}
	alertWebhook:=strings.TrimSpace(os.Getenv("BAFT_ALERT_WEBHOOK_URL"))
	if err:=app.ConfigureAlerts(bcc.AlertConfig{
		WebhookURL:alertWebhook,TelemetryStaleAfter:*telemetryStale,
		HandshakeErrorRateMilliPerMin:int64(*handshakeErrorRate*1000),
		Interval:*alertInterval,
	});err!=nil{fmt.Fprintln(os.Stderr,"alert config:",err);os.Exit(2)}

	ctx,stop:=signal.NotifyContext(context.Background(),os.Interrupt,syscall.SIGTERM)
	defer stop()
	go app.StartHealthLoop(ctx,*healthInterval)
	go app.StartAlertLoop(ctx)
	go app.StartAuditAnchorLoop(ctx)
	if strings.TrimSpace(os.Getenv("BAFT_BCC_BACKUP_KEY"))!=""{
		backupKey,err:=bcc.BackupKeyFromEnv()
		if err!=nil{fmt.Fprintln(os.Stderr,"backup key:",err);os.Exit(2)}
		go app.StartBackupLoop(ctx,*backupDir,backupKey,*backupInterval,bcc.BackupRetention{Daily:*backupDailyRetention,Weekly:*backupWeeklyRetention})
	}

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
