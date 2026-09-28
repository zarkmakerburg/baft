package bcc

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type FinanceReportRow struct {
	Period        string `json:"period"`
	Scope         string `json:"scope"`
	NodeID        string `json:"node_id,omitempty"`
	Currency      string `json:"currency"`
	IngressBytes  uint64 `json:"ingress_bytes"`
	EgressBytes   uint64 `json:"egress_bytes"`
	CostMicros    int64  `json:"cost_micros"`
	RevenueMicros int64  `json:"revenue_micros"`
	ProfitMicros  int64  `json:"profit_micros"`
}

func reportLocation(name string)(*time.Location,error){
	if strings.TrimSpace(name)==""{name="Asia/Tehran"}
	loc,err:=time.LoadLocation(name)
	if err!=nil{return nil,fmt.Errorf("invalid timezone: %w",err)}
	return loc,nil
}

func reportRange(from,to string,loc *time.Location)(time.Time,time.Time,error){
	if from==""||to==""{return time.Time{},time.Time{},errors.New("from and to dates are required")}
	start,err:=time.ParseInLocation("2006-01-02",from,loc)
	if err!=nil{return time.Time{},time.Time{},errors.New("from must be YYYY-MM-DD")}
	last,err:=time.ParseInLocation("2006-01-02",to,loc)
	if err!=nil{return time.Time{},time.Time{},errors.New("to must be YYYY-MM-DD")}
	if last.Before(start){return time.Time{},time.Time{},errors.New("to must not be before from")}
	return start,last.AddDate(0,0,1),nil
}

func (s *Store) FinanceReport(period,from,to,tz string)([]FinanceReportRow,error){
	if period!="daily"&&period!="monthly"{return nil,errors.New("period must be daily or monthly")}
	loc,err:=reportLocation(tz);if err!=nil{return nil,err}
	start,end,err:=reportRange(from,to,loc);if err!=nil{return nil,err}

	s.mu.Lock()
	ledger:=append([]FinanceLedgerEntry(nil),s.st.FinanceLedger...)
	s.mu.Unlock()

	type key struct{period,scope,node,currency string}
	rows:=map[key]FinanceReportRow{}
	for _,e:=range ledger{
		local:=e.Timestamp.In(loc)
		if local.Before(start)||!local.Before(end){continue}
		label:=local.Format("2006-01-02")
		if period=="monthly"{label=local.Format("2006-01")}
		currency:=e.Currency
		if currency==""{currency="IRR"}
		k:=key{label,"node",e.NodeID,currency}
		r:=rows[k]
		r.Period=label;r.Scope="node";r.NodeID=e.NodeID;r.Currency=currency
		r.IngressBytes+=e.IngressBytes;r.EgressBytes+=e.EgressBytes
		r.CostMicros=satAdd(r.CostMicros,e.CostMicros)
		r.RevenueMicros=satAdd(r.RevenueMicros,e.RevenueMicros)
		r.ProfitMicros=satAdd(r.ProfitMicros,e.ProfitMicros)
		rows[k]=r
	}
	// Derive cluster rows strictly from node rows so the equality is structural.
	nodeRows:=make([]FinanceReportRow,0,len(rows))
	for _,r:=range rows{nodeRows=append(nodeRows,r)}
	for _,r:=range nodeRows{
		k:=key{r.Period,"cluster","",r.Currency}
		c:=rows[k]
		c.Period=r.Period;c.Scope="cluster";c.Currency=r.Currency
		c.IngressBytes+=r.IngressBytes;c.EgressBytes+=r.EgressBytes
		c.CostMicros=satAdd(c.CostMicros,r.CostMicros)
		c.RevenueMicros=satAdd(c.RevenueMicros,r.RevenueMicros)
		c.ProfitMicros=satAdd(c.ProfitMicros,r.ProfitMicros)
		rows[k]=c
	}
	out:=make([]FinanceReportRow,0,len(rows))
	for _,r:=range rows{out=append(out,r)}
	sort.Slice(out,func(i,j int)bool{
		if out[i].Period!=out[j].Period{return out[i].Period<out[j].Period}
		if out[i].Scope!=out[j].Scope{return out[i].Scope<out[j].Scope}
		if out[i].NodeID!=out[j].NodeID{return out[i].NodeID<out[j].NodeID}
		return out[i].Currency<out[j].Currency
	})
	return out,nil
}

func FinanceReportCSV(rows []FinanceReportRow)([]byte,error){
	var b bytes.Buffer
	w:=csv.NewWriter(&b)
	if err:=w.Write([]string{
		"period","scope","node_id","ingress_bytes","egress_bytes",
		"cost_micros","revenue_micros","profit_micros","currency",
	});err!=nil{return nil,err}
	for _,r:=range rows{
		if err:=w.Write([]string{
			r.Period,r.Scope,r.NodeID,
			strconv.FormatUint(r.IngressBytes,10),strconv.FormatUint(r.EgressBytes,10),
			strconv.FormatInt(r.CostMicros,10),strconv.FormatInt(r.RevenueMicros,10),
			strconv.FormatInt(r.ProfitMicros,10),r.Currency,
		});err!=nil{return nil,err}
	}
	w.Flush()
	if err:=w.Error();err!=nil{return nil,err}
	return b.Bytes(),nil
}

func (s *Store) RateHistory(nodeID string) []FinancePolicy {
	s.mu.Lock();defer s.mu.Unlock()
	out:=append([]FinancePolicy(nil),s.st.RateHistory[nodeID]...)
	return out
}
