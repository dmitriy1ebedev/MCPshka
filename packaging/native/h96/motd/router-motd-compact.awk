# Compact H96 renderer, called by the refresh timer only. Never from PAM.
BEGIN {
 esc=sprintf("%c",27); z=esc "[0m"; cy=esc "[36m"; gr=esc "[32m"; ye=esc "[33m"; re=esc "[31m"; dim=esc "[2m"
 if (color!=1) z=cy=gr=ye=re=dim=""
}
function a(s,k,p,t,i) { i=index(s,k); if (!i) return ""; t=substr(s,i+length(k)); if(match(t,p))return substr(t,RSTART,RLENGTH); return "" }
function c(s,col) { return col s z }
function out(k,v) { print c(sprintf("%-4s",k),cy) "  " v }
function fixadd(s) {
 if(s=="")return
 count++
 if(fix=="")fix=s
 else if(length(fix)+length(s)+2<=39)fix=fix ", " s
 else hidden++
}
{
 if($0 ~ /^ *H96[[:space:]]*\|/) host=$0
 else if($0 ~ /^ *MCPshka[[:space:]]*\|/) mcp=$0
 else if($0 ~ /^ *Lane[[:space:]]*\|/) lane=$0
 else if($0 ~ /^ *Сеть[[:space:]]*\|/) net=$0
 else if($0 ~ /^ *Сервисы[[:space:]]*\|/) svc=$0
 else if(index($0,"swap ")>0 && index($0,"fix")>0) alert=$0
}
END {
 if(host=="") { out("H96",c("data unavailable",re)); out("FIX",c("check collector",ye)); print c("more: motd",dim); exit }
 load=a(host,"load ","[0-9]+([.][0-9]+)?")
 ram=a(host,"RAM ","[0-9]+([.][0-9]+)?/[0-9]+([.][0-9]+)?G")
 disk=a(host,"диск ","[0-9]+%")
 ht=host; while(match(ht,/[0-9]+C/)){temp=substr(ht,RSTART,RLENGTH);ht=substr(ht,RSTART+RLENGTH)}
 uptime=host;sub(/^.*\|[[:space:]]*/,"",uptime);sub(/[[:space:]]*-[[:space:]]*load.*$/,"",uptime)
 up=""
 if(match(uptime,/[0-9]+д/))up=substr(uptime,RSTART,RLENGTH-2) "d"
 if(match(uptime,/[0-9]+ч/))up=up substr(uptime,RSTART,RLENGTH-2) "h"
 if(up=="")up="?"
 if(swap_pct !~ /^[0-9]+$/)swap_pct=a(alert,"swap ","[0-9]+")
 if(swap_pct=="")swap_pct="?"
 ver=a(mcp,"ok ","[0-9]+([.][0-9]+)*")
 mok=(ver!="");if(!mok)ver="down"
 lok=(index(lane,"ok ")>0)
 # The detailed dashboard already applies Docker restart-policy and exit-code
 # exceptions (e.g. intentionally stopped torrserver). Reuse its verdict.
 # Both "ok 14/14" and "СБОЙ 13/14 - zigbee2mqtt" carry the count.
 sv=a(svc,"|","[0-9]+/[0-9]+")
 if(sv=="")sv="?"
 split(sv,n,"/")
 sok=(sv!="?" && n[1]==n[2] && index(svc,"ok")>0)
 badservice=""
 if(!sok && sv!="?") {
  badservice=svc
  sub(/^.*[0-9]+\/[0-9]+[[:space:]]*/, "", badservice)
  sub(/^[[:space:]]*-[[:space:]]*/, "", badservice)
  sub(/[[:space:]]*->[[:space:]]*fix.*$/, "", badservice)
  sub(/[[:space:]]+$/, "", badservice)
  # The first failed service/container takes priority in FIX.
  if(badservice!="" && badservice!=svc) {
   split(badservice,failed,",")
   firstbad=failed[1]
   sub(/^[[:space:]]+|[[:space:]]+$/, "", firstbad)
   if(length(firstbad)>22)firstbad=substr(firstbad,1,21) "~"
   badservice="svc " firstbad
   extra=0
   for(i in failed) if(i>1 && failed[i]!="")extra++
   if(extra>0)badservice=badservice " +" extra
  } else badservice=""
 }

 tok=(index(net,"Tailscale ok")>0)
 ip="?";if(match(net,/[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/))ip=substr(net,RSTART,RLENGTH)
 tc=(temp+0>=75?re:(temp+0>=65?ye:gr))
 sc=(swap_pct+0>=85?re:(swap_pct+0>=70?ye:gr))
 dc=(disk+0>=92?re:(disk+0>=85?ye:gr))
 out("H96","up " up "  load " (load!=""?load:"?") "  temp " c(temp!=""?temp:"?",tc))
 out("RAM",(ram!=""?ram:"?") "  swap " c(swap_pct "%",sc) "  disk " c(disk!=""?disk:"?",dc))
 out("MCP",c(ver (mok?" ok":" err"),mok?gr:re) "  Lane " c(lok?"ok":"err",lok?gr:re) "  svc " c(sv (sok?" ok":" err"),sok?gr:re))
 out("NET","TS " c(tok?"ok":"err",tok?gr:re) "  " ip)
 if(!sok)fixadd(badservice!=""?badservice:"services")
 if(temp!="" && temp+0>=65)fixadd("cool CPU " temp)
 if(swap_pct!="?" && swap_pct+0>=70)fixadd("swap " swap_pct "%")
 if(disk!="" && disk+0>=85)fixadd("disk " disk)
 if(!mok)fixadd("MCP")
 if(!lok)fixadd("Lane")
 if(!tok)fixadd("Tailscale")
 if(fix=="")fix="none"
 if(hidden) { if(length(fix)+5<=40)fix=fix " +" hidden;else fix="see motd +" hidden }
 out("FIX",c(fix,count?(!sok||temp+0>=75||swap_pct+0>=85||disk+0>=92?re:ye):gr))
 print c("more: motd",dim)
}
