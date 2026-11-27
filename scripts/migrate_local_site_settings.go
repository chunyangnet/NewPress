package main

import (
 "bytes"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "os"
 "time"
 "newpress.local/newpress/internal/site"
)
func main(){
 p:=os.Args[1]; raw,err:=os.ReadFile(p); must(err)
 var doc map[string]json.RawMessage; must(json.Unmarshal(raw,&doc))
 var compact bytes.Buffer; must(json.Compact(&compact,doc["settings"]))
 digest:=sha256.Sum256(compact.Bytes()); var revision string; must(json.Unmarshal(doc["revision"],&revision)); if revision!=hex.EncodeToString(digest[:8]){panic("original revision mismatch")}
 var settings site.Settings; must(json.Unmarshal(doc["settings"],&settings)); normalized,fields:=site.Validate(settings); if len(fields)>0{panic(fmt.Sprint(fields))}
 encoded,err:=json.Marshal(normalized);must(err)
 var before,after map[string]any;must(json.Unmarshal(doc["settings"],&before));must(json.Unmarshal(encoded,&after)); check(before,after,"")
 digest=sha256.Sum256(encoded); doc["revision"],_=json.Marshal(hex.EncodeToString(digest[:8]));doc["settings"]=encoded
 result,err:=json.MarshalIndent(doc,"","  ");must(err)
 out:=os.Args[2]
 must(os.MkdirAll(out,0700))
 backup:=out+"/site-settings.original-"+time.Now().Format("20060102-150405")+".json"
 f,err:=os.OpenFile(backup,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600);must(err);_,err=f.Write(raw);must(err);must(f.Close())
 candidate:=out+"/site-settings.migrated.json"
 f,err=os.OpenFile(candidate,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600);must(err);_,err=f.Write(append(result,'\n'));must(err);must(f.Close());fmt.Println("Prepared candidate:",candidate,"Backup:",backup)
}
func must(err error){if err!=nil{panic(err)}}
func check(before,after map[string]any,path string){for k,v:=range before{n,ok:=after[k];if !ok{panic("removed field "+path+k)};if m,ok:=v.(map[string]any);ok{nm,ok:=n.(map[string]any);if !ok{panic("changed type")};check(m,nm,path+k+".")}else{a,_:=json.Marshal(v);b,_:=json.Marshal(n);if !bytes.Equal(a,b){panic("changed existing field "+path+k)}}};for k:=range after{if _,ok:=before[k];!ok{fmt.Println("Added field:",path+k)}}}
