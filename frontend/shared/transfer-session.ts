import {object,text,integer} from './plugin-data-common';

export class TransferError extends Error {
 status=0; code=''; details:unknown; confirmed=false;
}
export const transferError = (error:unknown):string => error instanceof Error?error.message:'请求未完成，请核对结果。';

// Reads, file decoding and mutations belong to the account and page that began them.
export class TransferSession {
 readonly events=new AbortController();
 readonly identity:number;
 readonly readonly:boolean;
 active=true; lost=false; disposed=false; busy=false; uncertain=false;
 generation=0; epoch=0;
 private mutation=false;
 private readonly reads=new Set<AbortController>();
 private readonly writes=new Set<AbortController>();
 private channel:BroadcastChannel|undefined;
 constructor(private root:HTMLElement,private base:string,private changed:(lost:boolean)=>void,private resume:()=>void,private identityBody=true) {
  this.identity=Number(root.dataset.userId);
  this.readonly=root.dataset.readonly==='true'||!root.dataset.userId||!integer(this.identity,identityBody?0:1);
  const options={signal:this.events.signal};
  window.addEventListener('pagehide',()=>this.retire(),options);
  window.addEventListener('pageshow',event=>{if(event.persisted&&!this.lost&&!this.disposed){this.active=true;this.resume();}},options);
  document.addEventListener('chunnel:session',()=>this.loseAccess(),options);
  document.addEventListener('chunnel:admin-before-replace',event=>{
   if(event.detail?.regions.some(region=>region===root||region.contains(root)||root.contains(region))){this.disposed=true;this.retire();this.events.abort();this.channel?.close();}
  },options);
  try{this.channel=new BroadcastChannel('chunnel.social');this.channel.addEventListener('message',event=>{if(object(event.data)&&event.data.type==='session')this.loseAccess();});}catch{}
 }
 get locked():boolean {return this.readonly||!this.active||this.lost||this.disposed||this.busy||this.uncertain;}
 current(generation:number):boolean {return this.active&&!this.lost&&!this.disposed&&this.root.isConnected&&generation===this.generation;}
 private abort(controllers:Set<AbortController>):void {controllers.forEach(c=>c.abort());controllers.clear();}
 private retire():void {
  this.active=false;this.generation++;this.epoch++;this.abort(this.reads);this.abort(this.writes);
  if(this.busy&&this.mutation)this.uncertain=true;
  this.busy=false;this.mutation=false;this.changed(this.lost);
 }
 loseAccess():void {if(this.lost)return;this.lost=true;this.retire();this.channel?.close();}
 begin(mutation=true,retry=false):number|undefined {
  if(this.readonly||!this.current(this.generation)||this.busy||this.uncertain&&!retry)return;
  this.busy=true;this.mutation=mutation;this.epoch++;this.abort(this.reads);return this.generation;
 }
 finish(generation:number):void {if(this.current(generation)){this.busy=false;this.mutation=false;}}
 fail(error:unknown,mutation=true):string {
  if(error instanceof TransferError&&(error.status===401||error.status===403||error.code==='plugins.identity_changed'))this.loseAccess();
  else if(mutation&&!(error instanceof TransferError&&error.confirmed))this.uncertain=true;
  return this.lost?'登录或权限已变化，请重新打开页面。':this.uncertain?'未能确认操作结果，请先核对；不会自动重复操作。 '+transferError(error):transferError(error);
 }
 async request(path:string,method='GET',body?:object,timeout=30000,upload?:Blob):Promise<unknown> {
  const generation=this.generation,epoch=this.epoch;
  if(this.readonly||!this.current(generation))throw new Error('页面或登录状态已变化');
  const controller=new AbortController(),controllers=method==='GET'?this.reads:this.writes;controllers.add(controller);
  const timer=setTimeout(()=>controller.abort(),timeout);
  try {
   const response=await fetch(this.base+path,{method,credentials:'same-origin',cache:'no-store',redirect:'error',signal:controller.signal,
    headers:{Accept:'application/json',...(body||upload?{'X-CSRF-Token':this.root.dataset.csrf||'','Content-Type':upload?'application/octet-stream':'application/json'}:{})},
    body:upload|| (body?JSON.stringify(this.identityBody?{...body,expected_user_id:this.identity}:body):undefined)});
   const data:unknown=await response.json().catch(()=>null);
   if(!this.current(generation)||controller.signal.aborted||method==='GET'&&epoch!==this.epoch)throw new Error('请求已取消或页面已变化，结果待核对。');
   if(!response.ok){const error=new TransferError(object(data)&&object(data.error)&&text(data.error.message)?data.error.message:'请求未完成，请核对结果。');error.status=response.status;
    if(object(data)&&object(data.error)){error.code=text(data.error.code)?data.error.code:'';error.details=data.error.details;}
    error.confirmed=[400,401,403,409,429].includes(response.status);
    if(error.status===401||error.status===403||error.code==='plugins.identity_changed')this.loseAccess();throw error;
   }
   return data;
  }finally{clearTimeout(timer);controllers.delete(controller);}
 }
}
