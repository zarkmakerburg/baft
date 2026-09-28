package bcc

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeRegistrationUsesEnvTokenAndRejectsRawSecret(t *testing.T){
	store,err:=OpenStore(filepath.Join(t.TempDir(),"state.json"));if err!=nil{t.Fatal(err)}
	app,err:=NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	const envName="BAFT_TEST_NODE_TOKEN"
	const secret="node-env-secret-value"
	t.Setenv(envName,secret)

	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",map[string]any{
		"ID":"n-env","Alias":"Env Node","Address":"127.0.0.1:27001","Role":"foreign","AgentTokenEnv":envName,
	}))
	if rr.Code!=http.StatusCreated{t.Fatalf("env registration status=%d body=%s",rr.Code,rr.Body.String())}
	if strings.Contains(rr.Body.String(),secret)||strings.Contains(rr.Body.String(),tokenHash(secret)){t.Fatalf("secret/hash leaked: %s",rr.Body.String())}

	req:=httptest.NewRequest(http.MethodGet,"/api/agent/jobs?node_id=n-env",nil)
	req.Header.Set("Authorization","Bearer "+secret)
	rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("env token was not usable status=%d body=%s",rr.Code,rr.Body.String())}

	const rawSecret="raw-secret-must-not-be-accepted"
	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",map[string]any{
		"ID":"n-raw","Alias":"Raw Node","Address":"127.0.0.1:27002","Role":"foreign","AgentToken":rawSecret,
	}))
	if rr.Code!=http.StatusBadRequest{t.Fatalf("raw token accepted status=%d body=%s",rr.Code,rr.Body.String())}
	if strings.Contains(rr.Body.String(),rawSecret){t.Fatalf("raw secret echoed in error: %s",rr.Body.String())}
	t.Log("PASS node agent token sourced from env; raw token rejected and masked")
}
