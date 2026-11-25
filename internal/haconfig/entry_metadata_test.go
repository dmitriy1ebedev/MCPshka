package haconfig

import (
 "context"
 "encoding/json"
 "errors"
 "strings"
 "testing"
)

type mcp241SecretsFixture struct{ calls int; malicious bool; single bool }
func (f *mcp241SecretsFixture) REST(_ context.Context,method,path string,_ any,out any)error{
 f.calls++
 if method!="GET"||path!="/api/config/config_entries/entry"{
  return errors.New("unexpected mutation or secret read attempt")
 }
 entries:=[]map[string]any{
  {"entry_id":"meross-one","domain":"meross_lan","state":"setup_error",
   "title":"password:MY_SECRET","data":map[string]any{"host":"192.168.0.117","key":"MY_SECRET"}},
  {"entry_id":"meross-two","domain":"meross_lan","state":"loaded",
   "title":"key:ANOTHER_SECRET","options":map[string]any{"password":"ANOTHER_SECRET"}},
  {"entry_id":"homekit-one","domain":"homekit","state":"loaded","title":"Private Home"},
 }
 if f.malicious {entries[1]["entry_id"]="meross-one"}
 if f.single {entries=append(entries[:1],entries[2:]...)}
 raw,_:=json.Marshal(entries)
 return json.Unmarshal(raw,out)
}
func (f *mcp241SecretsFixture) WS(_ context.Context,_ ...map[string]any)([]json.RawMessage,error){
 return nil,errors.New("websocket not permitted for entry metadata inventory")
}
func TestMCP241RekeyPreflightIsReadOnlyAndRequiresUnambiguousOwnerApproval(t *testing.T) {
	ctx:=context.Background()
	ambiguous:=&mcp241SecretsFixture{}
	out,err:=ReadConfigEntryRekeyPreflight(ctx,ambiguous,"meross_lan","meross-one")
	if err!=nil||out.Decision!="HOLD_AMBIGUOUS_ENTRY_SET"||out.NeedsOwnerApproval||out.ActualMutation||out.DomainEntries!=2 {
		t.Fatalf("ambiguous Meross entries must HOLD: %+v err=%v",out,err)
	}
	unique:=&mcp241SecretsFixture{single:true}
	out,err=ReadConfigEntryRekeyPreflight(ctx,unique,"meross_lan","meross-one")
	if err!=nil||out.Decision!="OWNER_APPROVAL_REQUIRED"||!out.NeedsOwnerApproval||out.ActualMutation||out.State!="setup_error" {
		t.Fatalf("unique unavailable entry not gated: %+v err=%v",out,err)
	}
	out,err=ReadConfigEntryRekeyPreflight(ctx,unique,"meross_lan","meross-foreign")
	if err!=nil||out.Decision!="HOLD_TARGET_NOT_FOUND"||out.NeedsOwnerApproval {
		t.Fatalf("unknown Meross entry accepted: %+v err=%v",out,err)
	}
	for _,tc:=range []struct{domain,id string}{
		{"meross_lan","../.storage"},{"../.storage","meross-one"},
	}{
		before:=unique.calls
		if _,err:=ReadConfigEntryRekeyPreflight(ctx,unique,tc.domain,tc.id);err==nil||unique.calls!=before {
			t.Fatal("malformed scoped rekey preflight sent an HA request",tc)
		}
	}
	raw,_:=json.Marshal(out)
	if strings.Contains(string(raw),"MY_SECRET")||strings.Contains(string(raw),"192.168.0.117") {
		t.Fatal("rekey preflight leaked HA secrets")
	}
}

func TestMCP241ReadConfigEntryMetadataOnlySafeFields(t *testing.T){
 fixture:=&mcp241SecretsFixture{}
 got,err:=ReadConfigEntryMetadata(context.Background(),fixture,"meross_lan",200)
 if err!=nil||got.Total!=2||len(got.Entries)!=2||got.Truncated||!got.ReadOnly{
  t.Fatalf("metadata inventory: %+v err=%v",got,err)
 }
 if got.Entries[0].EntryID!="meross-one"||!got.Entries[0].NeedsAttention||
  got.Entries[1].NeedsAttention {t.Fatalf("meross entry states: %+v",got)}
 raw,_:=json.Marshal(got)
 for _,secret:=range []string{"MY_SECRET","ANOTHER_SECRET","192.168.0.117","password","title","options","key:"}{
  if strings.Contains(string(raw),secret){t.Fatalf("private material leaked: %s",secret)}
 }
 if fixture.calls!=1{t.Fatalf("not exactly one GET: %d",fixture.calls)}
 one,err:=ReadConfigEntryMetadata(context.Background(),fixture,"",1)
 if err!=nil||one.Total!=3||len(one.Entries)!=1||!one.Truncated{
  t.Fatalf("limit/paging not enforced: %+v err=%v",one,err)
 }
}
func TestMCP241ReadConfigEntryMetadataRejectsBadDomainAndDupIdentity(t *testing.T){
 fixture:=&mcp241SecretsFixture{malicious:true}
 got,err:=ReadConfigEntryMetadata(context.Background(),fixture,"meross_lan",25)
 if err==nil||len(got.Entries)==2{t.Fatalf("duplicate unsafe identity accepted: %+v",got)}
 for _,domain:=range []string{"../.storage","meross_lan?secrets=1","meross lan",strings.Repeat("a",80)} {
  n:=fixture.calls
  if _,err:=ReadConfigEntryMetadata(context.Background(),fixture,domain,2);err==nil{
   t.Fatalf("invalid domain accepted: %s",domain)
  }
  if fixture.calls!=n{t.Fatal("invalid input generated HA API call")}
 }
}
func TestMCP241ReadConfigEntryMetadataPropagatesNoServerSecrets(t *testing.T){
 f:=&mcp241Failing{}
 _,err:=ReadConfigEntryMetadata(context.Background(),f,"meross_lan",20)
 if err==nil||strings.Contains(err.Error(),"MY_SECRET"){t.Fatal("raw HA backend error leaked:",err)}
}
type mcp241Failing struct{}
func (*mcp241Failing) REST(context.Context,string,string,any,any)error{return errors.New("MY_SECRET response text")}
func (*mcp241Failing) WS(context.Context,...map[string]any)([]json.RawMessage,error){return nil,errors.New("no")}
