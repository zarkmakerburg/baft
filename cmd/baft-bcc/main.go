package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
)

func main(){
	if len(os.Args)>1&&os.Args[1]=="access"{os.Exit(runAccess(os.Args[2:]))}
	if len(os.Args)>1&&os.Args[1]=="jobkey"{os.Exit(runJobKey(os.Args[2:]))}
	if len(os.Args)>1&&os.Args[1]=="restore-preview"{os.Exit(runRestorePreview(os.Args[2:],os.Stdout,os.Stderr))}
	if len(os.Args)>1&&os.Args[1]=="verify-backup"{os.Exit(runVerifyBackup(os.Args[2:],os.Stdout,os.Stderr))}
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
	accessFile:=flag.String("access-file","","web access file (default <state-file>.access.json); create it with: baft-bcc access init")
	tlsCert:=flag.String("tls-cert","","TLS certificate file (PEM); re-read when it changes")
	tlsKey:=flag.String("tls-key","","TLS private key file (PEM)")
	jobKeyFile:=flag.String("job-key-file","","job-signing key (default <state-file>.job-key; created on first start)")
	installScript:=flag.String("install-script","","install.sh to push to new servers over SSH (enables /api/bootstrap together with --public-url)")
	publicURL:=flag.String("public-url","","URL agents use to reach this BCC, e.g. https://bcc.example.com (needed for /api/bootstrap)")
	allowInsecureHTTP:=flag.Bool("allow-insecure-http",false,"serve plain HTTP on a non-loopback address (not recommended)")
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
	useTLS:=*tlsCert!=""||*tlsKey!=""
	if useTLS&&(*tlsCert==""||*tlsKey==""){fmt.Fprintln(os.Stderr,"--tls-cert and --tls-key go together");os.Exit(2)}
	if !useTLS&&!isLoopbackListen(*listen)&&!*allowInsecureHTTP{
		fmt.Fprintln(os.Stderr,"BCC listen: a non-loopback address needs --tls-cert/--tls-key (or --allow-insecure-http)");os.Exit(2)
	}
	if *accessFile==""{*accessFile=*stateFile+".access.json"}
	if *jobKeyFile==""{*jobKeyFile=*stateFile+".job-key"}

	raw,err:=os.ReadFile(*adminTokenFile)
	if err!=nil{fmt.Fprintln(os.Stderr,"admin token:",err);os.Exit(1)}
	adminToken:=strings.TrimSpace(string(raw))
	if adminToken==""{fmt.Fprintln(os.Stderr,"admin token is empty");os.Exit(1)}

	// One BCC per state file; also what lets restore-preview know BCC is stopped.
	releaseState,err:=bcc.LockState(*stateFile)
	if err!=nil{fmt.Fprintln(os.Stderr,"BCC state lock:",err);os.Exit(1)}
	defer releaseState()
	store,err:=bcc.OpenStore(*stateFile)
	if err!=nil{fmt.Fprintln(os.Stderr,"BCC state:",err);os.Exit(1)}
	// Active tunnels are checked for drift this often; 0 turns the automatic check off.
	store.DriftEvery=time.Hour
	if v:=strings.TrimSpace(os.Getenv("BAFT_BCC_DRIFT_INTERVAL"));v!=""{
		d,err:=time.ParseDuration(v)
		if err!=nil||d<0||(d>0&&d<time.Minute){fmt.Fprintln(os.Stderr,"BAFT_BCC_DRIFT_INTERVAL must be a duration of at least 1m, or 0");os.Exit(2)}
		store.DriftEvery=d
	}
	app,err:=bcc.NewServer(store,adminToken)
	if err!=nil{fmt.Fprintln(os.Stderr,"BCC server:",err);os.Exit(1)}
	trustedProxies:=[]string{}
	for _,part:=range strings.Split(os.Getenv("BAFT_BCC_TRUSTED_PROXIES"),","){
		if v:=strings.TrimSpace(part);v!=""{trustedProxies=append(trustedProxies,v)}
	}
	if err:=app.ConfigureTrustedProxies(trustedProxies);err!=nil{fmt.Fprintln(os.Stderr,"trusted proxies:",err);os.Exit(2)}
	jobKey,created,err:=bcc.LoadOrCreateJobKey(*jobKeyFile)
	if err!=nil{fmt.Fprintln(os.Stderr,"job signing key:",err);os.Exit(1)}
	app.ConfigureJobSigning(jobKey)
	if *installScript!=""||*publicURL!=""{
		if *installScript==""||*publicURL==""{fmt.Fprintln(os.Stderr,"--install-script and --public-url go together");os.Exit(2)}
		if !strings.HasPrefix(*publicURL,"https://")&&!(*allowInsecureHTTP&&strings.HasPrefix(*publicURL,"http://")){
			fmt.Fprintln(os.Stderr,"--public-url must be https:// (http:// only with --allow-insecure-http)");os.Exit(2)
		}
		script,err:=os.ReadFile(*installScript)
		if err!=nil||len(script)==0{fmt.Fprintln(os.Stderr,"install script:",err);os.Exit(1)}
		app.ConfigureBootstrap(bcc.BootstrapConfig{InstallScript:script,PublicURL:*publicURL,AllowHTTP:*allowInsecureHTTP})
	}
	if created{fmt.Printf("created job signing key %s; agents pin its public key: %s\n",*jobKeyFile,app.JobPublicKey())}
	if err:=app.ConfigureAccess(*accessFile,useTLS);err!=nil{
		fmt.Fprintf(os.Stderr,"BCC access: %v\ncreate it on this host with: baft-bcc access init --access-file %s\n",err,*accessFile);os.Exit(2)
	}
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
	app.StartTunnelLoop(ctx,2*time.Second)
	go app.StartAuditAnchorLoop(ctx)
	if strings.TrimSpace(os.Getenv("BAFT_BCC_BACKUP_KEY"))!=""{
		backupKey,err:=bcc.BackupKeyFromEnv()
		if err!=nil{fmt.Fprintln(os.Stderr,"backup key:",err);os.Exit(2)}
		if err:=app.ConfigureBackupAdmin(*backupDir,backupKey);err!=nil{fmt.Fprintln(os.Stderr,"backup admin:",err);os.Exit(2)}
		go app.StartBackupLoop(ctx,*backupDir,backupKey,*backupInterval,bcc.BackupRetention{Daily:*backupDailyRetention,Weekly:*backupWeeklyRetention})
	}

	srv:=&http.Server{Addr:*listen,Handler:app.Handler(),ReadHeaderTimeout:5*time.Second}
	done:=make(chan error,1)
	if useTLS{
		certs:=&reloadingCert{certFile:*tlsCert,keyFile:*tlsKey}
		if _,err:=certs.get(nil);err!=nil{fmt.Fprintln(os.Stderr,"TLS:",err);os.Exit(2)}
		srv.TLSConfig=&tls.Config{MinVersion:tls.VersionTLS12,GetCertificate:certs.get}
		go func(){done<-srv.ListenAndServeTLS("","")}()
		fmt.Printf("BAFT Command Center listening on https://%s (dashboard under its secret path; see: baft-bcc access show)\n",*listen)
	}else{
		go func(){done<-srv.ListenAndServe()}()
		fmt.Printf("BAFT Command Center listening on http://%s (dashboard under its secret path; see: baft-bcc access show)\n",*listen)
	}

	select{
	case <-ctx.Done():
		shutdownCtx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	case err:=<-done:
		if err!=nil&&err!=http.ErrServerClosed{fmt.Fprintln(os.Stderr,"BCC stopped:",err);os.Exit(1)}
	}
}

func isLoopbackListen(addr string) bool {
	host,_,err:=net.SplitHostPort(addr)
	if err!=nil{return false}
	if host=="localhost"{return true}
	ip:=net.ParseIP(host)
	return ip!=nil&&ip.IsLoopback()
}
