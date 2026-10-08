package integration_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

type stageENetemProfile struct {
	RTTMS    int
	LossPct  string
	RateMbit int
}

func stageEEnvInt(name string, fallback int) int {
	raw:=strings.TrimSpace(os.Getenv(name))
	if raw=="" { return fallback }
	v,err:=strconv.Atoi(raw)
	if err!=nil || v<=0 { return fallback }
	return v
}

func stageENetemFromEnv() (stageENetemProfile,bool) {
	rtt:=stageEEnvInt("BAFT_STAGE_E_NETEM_RTT_MS",0)
	rate:=stageEEnvInt("BAFT_STAGE_E_NETEM_RATE_MBIT",0)
	loss:=strings.TrimSpace(os.Getenv("BAFT_STAGE_E_NETEM_LOSS_PCT"))
	if loss=="" { loss="0" }
	if rtt<=0 && rate<=0 && (loss=="0" || loss=="0.0") { return stageENetemProfile{},false }
	if rtt<=0 || rate<=0 { return stageENetemProfile{},false }
	return stageENetemProfile{RTTMS:rtt,LossPct:loss,RateMbit:rate},true
}

func stageEApplyNetem(t *testing.T, addr string) (stageENetemProfile,func()) {
	t.Helper()
	p,enabled:=stageENetemFromEnv()
	if !enabled { return stageENetemProfile{},func(){} }
	_,portRaw,err:=net.SplitHostPort(addr)
	if err!=nil { t.Fatalf("netem split addr %q: %v",addr,err) }
	port,err:=strconv.Atoi(portRaw)
	if err!=nil { t.Fatalf("netem port %q: %v",portRaw,err) }

	run:=func(args ...string){
		t.Helper()
		cmd:=exec.Command("sudo",append([]string{"-n","tc"},args...)...)
		out,err:=cmd.CombinedOutput()
		if err!=nil { t.Fatalf("tc %s: %v: %s",strings.Join(args," "),err,strings.TrimSpace(string(out))) }
	}
	cleanup:=func(){
		_ = exec.Command("sudo","-n","tc","qdisc","del","dev","lo","root").Run()
	}
	cleanup()
	run("qdisc","add","dev","lo","root","handle","1:","prio","bands","3")
	netemArgs:=[]string{"qdisc","add","dev","lo","parent","1:3","handle","30:","netem",
		"delay",fmt.Sprintf("%.3fms",float64(p.RTTMS)/2),
		"loss",p.LossPct+"%",
		"rate",fmt.Sprintf("%dmbit",p.RateMbit)}
	run(netemArgs...)
	run("filter","add","dev","lo","protocol","ip","parent","1:","prio","3","u32",
		"match","ip","protocol","6","0xff","match","ip","dport",strconv.Itoa(port),"0xffff","flowid","1:3")
	run("filter","add","dev","lo","protocol","ip","parent","1:","prio","3","u32",
		"match","ip","protocol","6","0xff","match","ip","sport",strconv.Itoa(port),"0xffff","flowid","1:3")
	t.Cleanup(cleanup)
	return p,cleanup
}
