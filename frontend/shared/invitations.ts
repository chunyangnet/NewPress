import {required,pageRequests,failure} from './collection-runtime';
import {invitations,invitationReceipt} from './form-data';
import type {} from './site-time-global';

(() => {
  const page=document.querySelector<HTMLElement>('[data-invitations]');if(!page)return;
  const root=page,form=required<HTMLFormElement>(root,'form'),email=required<HTMLInputElement>(form,'[name=email]');
  const status=required(root,'[data-invitation-status]'),list=required(root,'[data-invitation-list]');
  const send=required<HTMLButtonElement>(form,'button[type=submit]'),refresh=required<HTMLButtonElement>(root,'[data-invitation-refresh]');
  let serial=0,busy=false,allowed=false,needsReview=false;
  const revoked=new Set<string>();
  let reader:AbortController|undefined;
  const requests=pageRequests(Number(root.dataset.userId),()=>{
    serial++;reader?.abort();list.replaceChildren();allowed=false;
    root.querySelectorAll<HTMLButtonElement|HTMLInputElement>('button,input').forEach(node=>node.disabled=true);
    status.textContent='登录状态已变化，邮箱输入已保留，请重新打开页面。';
  });
  const node=<K extends keyof HTMLElementTagNameMap>(tag:K,text:string,className=''):HTMLElementTagNameMap[K]=>{
    const result=document.createElement(tag);result.textContent=text;result.className=className;return result;
  };
  const controls=()=>{
    if(requests.lost)return;
    send.disabled=busy||!allowed||needsReview;
    refresh.disabled=busy;
    list.querySelectorAll<HTMLButtonElement>('button').forEach(button=>button.disabled=busy||needsReview);
  };
  const api=(path='',body?:{email:string}|{id:string},signal?:AbortSignal)=>requests.request('/api/v1/admin/invitations'+path,{
    signal,...(body?{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':root.dataset.csrf||''},body:JSON.stringify(body)}:{})
  },12000);
  const unauthorized=(problem:unknown)=>{
    const error=failure(problem);if(error.status===401||error.status===403)requests.cancel();return error;
  };
  async function load(confirmed='') {
    const request=++serial;reader?.abort();reader=new AbortController();allowed=false;controls();
    try {
      const data=invitations(await api('',undefined,reader.signal));if(request!==serial||requests.lost)return;
      const rows=data.items.map(item=>{
        const row=node('div','','invitation-row');row.dataset.invitationId=item.id;
        const state=revoked.has(item.id)?'closed':item.state;
        row.append(node('strong',item.email),node('small','有效至 '+(window.ChunnelTime?.formatDateTime(item.expires_at)||'')),node('span',{pending:'待加入',closed:'已使用或撤销',expired:'已过期'}[state]));
        if(state==='pending'){
          const revoke=node('button','撤销');revoke.type='button';
          revoke.addEventListener('click',()=>mutate({id:item.id}));row.append(revoke);
        }
        return row;
      });
      list.replaceChildren(...rows);allowed=data.available&&data.registration!=='closed';needsReview=false;
      const availability=!data.available?'请先配置站点的 SMTP 邮件服务与公开 HTTPS 地址。':data.registration==='closed'?'请先在站点设置中选择邀请注册或开放注册。':'';
      status.textContent=[confirmed,availability].filter(Boolean).join(' ')|| (data.items.length?'':'还没有邀请。输入邮箱，邀请第一位读者加入。');
    }catch(problem){
      if(request!==serial||requests.lost)return;
      const error=unauthorized(problem);if(requests.lost)return;
      status.textContent=confirmed?confirmed+' 列表暂时无法刷新，请稍后刷新查看。':needsReview?'上次操作结果尚未确认，请刷新列表核对。 '+error.message:error.message;
    }finally{if(request===serial)controls();}
  }
  async function mutate(body:{email:string}|{id:string}) {
    if(busy||requests.lost||needsReview||('email'in body&&!allowed))return;
    busy=true;serial++;reader?.abort();controls();
    const sending='email'in body;
    status.textContent=sending?'正在发送邀请…':'正在撤销邀请…';
    try {
      invitationReceipt(await api(sending?'':'/revoke',body));
      if(sending&&email.value.trim()===body.email)email.value='';
      if('id'in body){
        revoked.add(body.id);
        const row=list.querySelector<HTMLElement>(`[data-invitation-id="${body.id}"]`);
        row?.querySelector('button')?.remove();const state=row?.querySelector('span');if(state)state.textContent='已使用或撤销';
      }
      const confirmed=sending?'邀请邮件已交给邮件服务。对方可在七天内确认并创建账号。':'邀请已撤销。';
      status.textContent=confirmed;await load(confirmed);
    }catch(problem){
      const error=unauthorized(problem);if(requests.lost)return;
      needsReview=!error.status||error.status>=500;
      status.textContent=needsReview?'操作结果尚未确认，邮箱输入已保留。请刷新列表核对，避免重复发送。':error.message;
    }finally{busy=false;controls();}
  }
  form.addEventListener('submit',event=>{event.preventDefault();if(!send.disabled&&form.reportValidity())void mutate({email:email.value.trim()});});
  refresh.addEventListener('click',()=>{if(busy||requests.lost)return;status.textContent='正在载入…';void load();});
  document.addEventListener('chunnel:session',event=>{if(!requests.lost&&event.detail?.id===Number(root.dataset.userId)&&event.detail.csrf_token)root.dataset.csrf=event.detail.csrf_token;});
  void load();
})();
