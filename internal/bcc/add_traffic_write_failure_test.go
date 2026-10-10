package bcc

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAddTrafficFailedSaveRestoresFinanceRemainderAndLedger(t *testing.T) {
	dir:=t.TempDir()
	dbPath:=filepath.Join(dir,"state.db")
	store,err:=OpenStore(dbPath)
	if err!=nil{t.Fatal(err)}
	const token="agent-secret"
	if _,err:=store.UpsertNode(Node{ID:"node",Address:"127.0.0.1:27000",Role:"foreign"},token);err!=nil{t.Fatal(err)}
	if err:=store.SetFinancePolicy("node",100,200);err!=nil{t.Fatal(err)}
	if _,err:=store.AddTraffic("node",token,1,0);err!=nil{t.Fatal(err)}
	beforeFinance:=store.st.Finance["node"]
	beforeRemainder:=store.st.FinanceRemainders["node"]
	beforeLedger:=append([]FinanceLedgerEntry(nil),store.st.FinanceLedger...)

	blocked:=filepath.Join(dir,"state-directory")
	if err:=os.Mkdir(blocked,0700);err!=nil{t.Fatal(err)}
	store.path=blocked
	if _,err:=store.AddTraffic("node",token,1,0);err==nil{t.Fatal("injected save failure unexpectedly succeeded")}
	if store.st.Finance["node"]!=beforeFinance ||
		store.st.FinanceRemainders["node"]!=beforeRemainder ||
		!reflect.DeepEqual(store.st.FinanceLedger,beforeLedger){
		t.Fatalf("failed save leaked finance mutation: finance=%+v remainder=%+v ledger=%+v",
			store.st.Finance["node"],store.st.FinanceRemainders["node"],store.st.FinanceLedger)
	}

	store.path=dbPath
	reopened,err:=OpenStore(dbPath)
	if err!=nil{t.Fatal(err)}
	if reopened.st.Finance["node"]!=beforeFinance ||
		reopened.st.FinanceRemainders["node"]!=beforeRemainder ||
		!reflect.DeepEqual(reopened.st.FinanceLedger,beforeLedger){
		t.Fatal("failed traffic write reached disk")
	}
	if _,err:=store.AddTraffic("node",token,1,0);err!=nil{t.Fatal(err)}
	reopened,err=OpenStore(dbPath)
	if err!=nil{t.Fatal(err)}
	if reopened.st.Finance["node"].IngressBytes!=2 || len(reopened.st.FinanceLedger)!=2 {
		t.Fatalf("retry accounting lost or duplicated: finance=%+v ledger=%+v",
			reopened.st.Finance["node"],reopened.st.FinanceLedger)
	}
}
