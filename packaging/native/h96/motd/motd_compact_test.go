package motd

import (
 "bytes"
 "os"
 "path/filepath"
 "regexp"
 "strings"
 "testing"
 "time"
 "unicode/utf8"
)

// This is a captured-shape fixture, not a live or invented health reading.
func compactDashboard(t *testing.T) string {
 t.Helper()
 return script(t, t.TempDir(), "dashboard", "#!/bin/sh\ncat <<'DASH'\n" +
  " H96      | 2д 14ч 49м - load 2.19 - RAM 1.9/3.7G - swap 1.4G - диск 74% - 67°C\n" +
  " ! fix    | CPU 67°C\n" +
  " ! fix    | память: доступно 47%, swap 76% - больше всех: python3\n" +
  " MCPshka  | ok 0.11.8 - песочница свободна - Face ID до 13.11 08:30\n" +
  " Сеть     | 192.168.77.10 -> 203.0.113.20 - Tailscale ok - VPN 2627 доменов\n" +
  " Lane     | ok 1.5.1 - DNS: политика Lane - выходы 10/10\n" +
  " Сервисы  | ok 14/14\nDASH\n")
}

func TestCompactMobileExactFields(t *testing.T) {
 needTools(t)
 out := refresh(t, compactDashboard(t), 10*time.Second, "ROUTER_MOTD_SWAP_PCT=76")
 assertSafe(t, out, 1000)
 text := strings.TrimSpace(string(out))
 for _, want := range []string{"up 2d14h", "load 2.19", "temp 67C", "1.9/3.7G", "swap 76%", "disk 74%", "0.11.8 ok", "Lane ok", "14/14", "TS ok", "192.168.77.10", "FIX", "cool CPU 67C", "more: motd"} {
  if !strings.Contains(text,want) { t.Errorf("missing %q from %q",want,text) }
 }
 for _, no := range []string{"Face ID","2627","python3","больше всех"} {
  if strings.Contains(text,no){ t.Errorf("extra detail %q leaked",no) }
 }
 lines:=strings.Split(text,"\n")
 if len(lines)!=6 { t.Fatalf("want six compact lines, got %d: %q",len(lines),text) }
 for _,line:=range lines {
  if utf8.RuneCountInString(line)>48 {t.Errorf("line wider than 48 columns: %q",line)}
 }
}

func TestCompactColorOnlySGRAndFastLogin(t *testing.T) {
 needTools(t)
 dir:=t.TempDir()
 snap:=filepath.Join(dir,"snapshot")
 render,err:=filepath.Abs("router-motd-compact.awk")
 if err!=nil {t.Fatal(err)}
 env:=[]string{
  "ROUTER_MOTD_SNAPSHOT="+snap,
  "ROUTER_MOTD_RENDER="+render,
  "ROUTER_MOTD_DASHBOARD="+compactDashboard(t),
  "ROUTER_MOTD_SWAP_PCT=76",
  "ROUTER_MOTD_OFF="+filepath.Join(dir,"off"),
  "ROUTER_MOTD_COLOR=always",
 }
 run(t,10*time.Second,env,"bash","router-motd-refresh","--force")
 color,err:=os.ReadFile(snap);if err!=nil{t.Fatal(err)}
 plain,err:=os.ReadFile(snap+".plain");if err!=nil{t.Fatal(err)}
 assertSafe(t,plain,1000)
 if !bytes.Contains(color,[]byte("\x1b[36m")) || !bytes.Contains(color,[]byte("\x1b[33m")) {
  t.Fatalf("missing simple ANSI colors: %q",color)
 }
 sgr:=regexp.MustCompile("\\x1b\\[(0|2|31|32|33|36)m")
 stripped:=sgr.ReplaceAll(color,nil)
 if bytes.Contains(stripped,[]byte("\x1b")) || !bytes.Equal(stripped,plain) {
  t.Fatalf("control leak or colored/plain mismatch: %q versus %q",color,plain)
 }
 for i:=0;i<5;i++ {
  got:=run(t,500*time.Millisecond,env,"sh","router-motd-login")
  if !bytes.Equal(got,color) {t.Fatalf("color read differs on run %d",i)}
 }
 got:=run(t,500*time.Millisecond,append(env,"ROUTER_MOTD_COLOR=never"),"sh","router-motd-login")
 if !bytes.Equal(got,plain){t.Fatalf("NO_COLOR read differs")}
}

func TestCompactCriticalWarnings(t *testing.T) {
 needTools(t)
 out:=refresh(t,compactDashboard(t),10*time.Second,"ROUTER_MOTD_SWAP_PCT=92")
 text:=string(out)
 if !strings.Contains(text,"swap 92%") || !strings.Contains(text,"FIX") {
  t.Fatalf("critical swap warning missing: %q",out)
 }
}

func serviceDashboard(t *testing.T, status string) string {
 t.Helper()
 content := "#!/bin/sh\ncat <<'DASH'\n" +
  " H96      | 2д 15ч 44м - load 1.33 - RAM 1.7/3.7G - swap 1.4G - диск 74% - 64°C\n" +
  " MCPshka  | ok 0.11.8 - idle\n" +
  " Сеть     | 192.168.77.10 -> 203.0.113.21 - Tailscale ok\n" +
  " Lane     | ok 1.5.1\n" +
  " Сервисы  | " + status + "\nDASH\n"
 return script(t,t.TempDir(),"service-dashboard",content)
}

func TestContainerStatusHealthyAndIntentionalStop(t *testing.T) {
 needTools(t)
 // The rich dashboard excludes a consciously stopped container
 // (restart=no, exit 0/143), so its aggregate remains healthy.
 out:=refresh(t,serviceDashboard(t,"ok 14/14"),10*time.Second,"ROUTER_MOTD_SWAP_PCT=0")
 assertSafe(t,out,1000)
 plain:=string(out)
 if !strings.Contains(plain,"svc 14/14 ok") || !strings.Contains(plain,"FIX   none") {
  t.Fatalf("expected healthy services and no fix: %q",plain)
 }
 if strings.Contains(plain,"torrserver") {
  t.Fatalf("intentionally stopped torrserver unexpectedly named: %q",plain)
 }
}

func TestContainerStatusShowsFailedNameAndCount(t *testing.T) {
 needTools(t)
 cases:=[]struct{status string; want []string}{
  {"СБОЙ 13/14 - zigbee2mqtt -> fix",[]string{"svc 13/14 err","FIX   svc zigbee2mqtt"}},
  {"СБОЙ 12/14 - zigbee2mqtt, mosquitto -> fix",[]string{"svc 12/14 err","FIX   svc zigbee2mqtt +1"}},
  {"СБОЙ 13/14 - homeassistant (unhealthy) -> fix",[]string{"svc 13/14 err","FIX   svc homeassistant"}},
  {"СБОЙ 13/14 - docker -> fix",[]string{"svc 13/14 err","FIX   svc docker"}},
  {"СБОЙ 13/14",[]string{"svc 13/14 err","FIX   services"}},
 }
 for _,tc:=range cases {
  t.Run(tc.status,func(t *testing.T){
   out:=refresh(t,serviceDashboard(t,tc.status),10*time.Second,"ROUTER_MOTD_SWAP_PCT=0")
   assertSafe(t,out,1000)
   for _,want:=range tc.want{if !strings.Contains(string(out),want){t.Errorf("expected %q in %q",want,out)}}
   for _,line:=range strings.Split(strings.TrimSpace(string(out)),"\n"){
    if utf8.RuneCountInString(line)>48{t.Errorf("line width exceeds 48: %q",line)}
   }
  })
 }
}

func TestContainerFailurePriorityWhenOtherWarningsPresent(t *testing.T) {
 needTools(t)
 out:=refresh(t,serviceDashboard(t,"СБОЙ 13/14 - zigbee2mqtt -> fix"),10*time.Second,"ROUTER_MOTD_SWAP_PCT=79")
 text:=string(out)
 if !strings.Contains(text,"FIX   svc zigbee2mqtt"){
  t.Fatalf("failed container must not be hidden by swap/CPU warnings: %q",text)
 }
}
