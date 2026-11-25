package haconfig

import (
 "context"
 "errors"
 "regexp"
 "sort"
 "strings"
)

// MCP-241: a read-only entry inventory uses HA's normal REST API and exposes
// only safe identity/status fields. It deliberately does not return title,
// host, key, options, pairing code, private .storage or flow input fields.
type ConfigEntryMetadata struct {
 EntryID string `json:"entry_id"`
 Domain string `json:"domain"`
 State string `json:"state"`
 NeedsAttention bool `json:"needs_attention"`
}
type ConfigEntryMetadataResult struct {
 Trust string `json:"trust"`
 ReadOnly bool `json:"read_only"`
 Entries []ConfigEntryMetadata `json:"entries"`
 Total int `json:"total"`
 Truncated bool `json:"truncated"`
}
var entryIDPattern=regexp.MustCompile(`^[a-zA-Z0-9_-]{1,96}$`)
var entryDomainPattern=regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)
var entryStatePattern=regexp.MustCompile(`^[a-z_]{1,40}$`)

// ReadConfigEntryMetadata never initiates a reconfigure flow. It can identify
// an existing unavailable integration entry and detect duplicates without
// exposing secrets from the full HA response.
func ReadConfigEntryMetadata(ctx context.Context,api API,domain string,limit int)(ConfigEntryMetadataResult,error){
 out:=ConfigEntryMetadataResult{Trust:"UNTRUSTED",ReadOnly:true,Entries:[]ConfigEntryMetadata{}}
 if domain!=""&&!entryDomainPattern.MatchString(domain){return out,errors.New("invalid integration domain")}
 if limit<=0||limit>200 {limit=100}
 entries,err:=ConfigEntries(ctx,api)
 if err!=nil{return out,errors.New("Home Assistant config-entry metadata unavailable")}
 if len(entries)>4096 {return out,errors.New("config-entry inventory exceeds bound")}
 seen:=map[string]bool{}
 for _,row:=range entries {
  if !entryIDPattern.MatchString(row.EntryID)||
   !entryDomainPattern.MatchString(row.Domain)||
   !entryStatePattern.MatchString(row.State)||
   seen[row.EntryID] {return out,errors.New("invalid or duplicate config-entry metadata")}
  seen[row.EntryID]=true
  if domain!=""&&row.Domain!=domain {continue}
  out.Total++
  if len(out.Entries)>=limit {out.Truncated=true;continue}
  out.Entries=append(out.Entries,ConfigEntryMetadata{
   EntryID:row.EntryID,Domain:row.Domain,State:row.State,
   NeedsAttention:!strings.EqualFold(row.State,"loaded"),
  })
 }
 sort.Slice(out.Entries,func(i,j int)bool{
  if out.Entries[i].Domain!=out.Entries[j].Domain {
   return out.Entries[i].Domain<out.Entries[j].Domain
  }
  return out.Entries[i].EntryID<out.Entries[j].EntryID
 })
 return out,nil
}

// ConfigEntryRekeyPreflight is an observation, not permission to edit HA
// credentials. It refuses to guess which Meross entry belongs to a device.
type ConfigEntryRekeyPreflight struct {
 EntryID string `json:"entry_id"`
 Domain string `json:"domain"`
 State string `json:"state,omitempty"`
 DomainEntries int `json:"domain_entries"`
 Decision string `json:"decision"`
 NeedsOwnerApproval bool `json:"needs_owner_approval"`
 ActualMutation bool `json:"actual_mutation"`
}

// ReadConfigEntryRekeyPreflight is fail-closed for multiple entries, an
// unrecognized target or a loaded entry. It never starts a flow.
func ReadConfigEntryRekeyPreflight(ctx context.Context, api API, domain, entryID string) (ConfigEntryRekeyPreflight,error) {
 out:=ConfigEntryRekeyPreflight{EntryID:entryID,Domain:domain,Decision:"HOLD",ActualMutation:false}
 if !entryDomainPattern.MatchString(domain)||!entryIDPattern.MatchString(entryID) {
  return out,errors.New("invalid scoped config entry identity")
 }
 inventory,err:=ReadConfigEntryMetadata(ctx,api,domain,200)
 if err!=nil{return out,err}
 out.DomainEntries=inventory.Total
 if inventory.Truncated||inventory.Total!=1 {
  out.Decision="HOLD_AMBIGUOUS_ENTRY_SET"
  return out,nil
 }
 item:=inventory.Entries[0]
 if item.EntryID!=entryID {
  out.Decision="HOLD_TARGET_NOT_FOUND"
  return out,nil
 }
 out.State=item.State
 if item.State=="loaded" {
  out.Decision="HOLD_ALREADY_LOADED"
  return out,nil
 }
 if item.State!="setup_error"&&item.State!="setup_retry" {
  out.Decision="HOLD_STATE_UNVERIFIED"
  return out,nil
 }
 out.Decision="OWNER_APPROVAL_REQUIRED"
 out.NeedsOwnerApproval=true
 return out,nil
}
