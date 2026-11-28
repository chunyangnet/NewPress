import {PluginRequests} from './plugin-requests';
import {object,integer,text,PluginRequestError,pluginError} from './plugin-data-common';

// The two workspaces share account changes, but own independent reads/writes.
export class CDNSession {
 readonly reads=new PluginRequests();
 readonly writes=new PluginRequests();
 readonly events=new AbortController();
 readonly readonly:boolean;
 readonly identity:number;
 active=true;
 disposed=false;
 accessLost=false;
 busy=false;
 uncertain=false;
 epoch=0;
 generation=0;
 private channel:BroadcastChannel|undefined;
 constructor(private page:HTMLElement,private root:HTMLElement,private base:string,private changed:(lost:boolean)=>void,private resume:()=>void) {
  this.identity=Number(page.dataset.userId);
  // Environment-only administrators have user ID 0 in the Go auth contract.
  this.readonly=page.dataset.readonly==='true'||!page.dataset.userId||!integer(this.identity);
  const options={signal:this.events.signal};
  window.addEventListener('pagehide',()=>this.retire(),options);
  window.addEventListener('pageshow',event=>{if(event.persisted&&!this.accessLost&&!this.disposed){this.active=true;this.reads.resume();this.writes.resume();this.resume();}},options);
  const session=()=>this.loseAccess();
  document.addEventListener('chunnel:session',session,options);
  page.addEventListener('cdn:access-lost',()=>{if(!this.accessLost)this.loseAccess();},options);
  try{this.channel=new BroadcastChannel('chunnel.social');this.channel.addEventListener('message',event=>{if(object(event.data)&&event.data.type==='session')session();});}catch{}
  document.addEventListener('chunnel:admin-before-replace',event=>{
   if(event.detail?.regions.some(region=>region===root||region.contains(root)||root.contains(region))){this.disposed=true;this.retire();this.events.abort();this.channel?.close();}
  },options);
 }
 get locked():boolean {return this.readonly||!this.active||this.disposed||this.accessLost||this.busy||this.uncertain;}
 private retire():void {
  this.active=false;this.generation++;this.epoch++;this.reads.suspend();this.writes.suspend();
  if(this.busy)this.uncertain=true;this.busy=false;this.changed(this.accessLost);
 }
 loseAccess():void {
  if(this.accessLost)return;this.accessLost=true;this.retire();this.channel?.close();
  this.page.dispatchEvent(new Event('cdn:access-lost'));
 }
 start():number|undefined {
  if(this.locked)return;
  this.busy=true;this.epoch++;this.reads.suspend();this.reads.resume();return this.generation;
 }
 current(generation:number):boolean {return this.active&&!this.disposed&&!this.accessLost&&generation===this.generation;}
 fail(error:unknown):string {
  const e=pluginError(error);
  if(e.status===401||e.status===403||e.code==='plugins.identity_changed')this.loseAccess();
  else if(!e.confirmedRejection)this.uncertain=true;
  return this.accessLost?'登录或权限已变化，请重新打开页面。':this.uncertain?'未能确认操作结果。请核对运行记录和云端回执后重新打开页面；不会自动重发。 '+e.message:e.message;
 }
 async api(path:string,method='GET',body?:object):Promise<unknown> {
  if(this.readonly||this.accessLost||!this.active||this.disposed)throw new Error('页面或登录状态已变化');
  const response=await (method==='GET'?this.reads:this.writes).json(this.base+path,{method,headers:{Accept:'application/json',...(body?{'Content-Type':'application/json','X-CSRF-Token':this.page.dataset.csrf||''}:{})},body:body?JSON.stringify({...body,expected_user_id:this.identity}):undefined});
  if(!response.ok){const source=object(response.data)&&object(response.data.error)?response.data.error:{};const error=new PluginRequestError(text(source.message)?source.message:'CDN 请求未完成');error.status=response.status;error.code=text(source.code)?source.code:'';error.confirmedRejection=[400,401,403,409,422,429].includes(response.status);if(error.status===401||error.status===403||error.code==='plugins.identity_changed')this.loseAccess();throw error;}
  return response.data;
 }
}
