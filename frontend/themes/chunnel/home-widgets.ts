// Loaded only after an explicit homepage interaction.
import type {ClientSession} from '../../shared/newpress-client';
import type {CurrencyPending} from '../../shared/currency-types';
import type {ProgressionPending} from '../../shared/progression-types';
import {id, nonce, failure} from '../../shared/ledger-data';
import {readPending, savePending, clearPending, definiteRejection} from '../../shared/currency-pending';
import {read, save, clear, rejected} from '../../shared/progression-pending';
import {parseWalletReward, parseProgressionReward, walletRewardReceipt, progressionRewardReceipt} from '../../shared/reward-data';

export function mountHomeRewards(): void {
  const card = document.querySelector<HTMLElement>('[data-home-checkin-card]');
  if (!card || !window.ChunnelInteractions) return;
  const trigger = card.querySelector<HTMLButtonElement>('[data-home-checkin]'), statusNode = card.querySelector<HTMLElement>('[data-home-checkin-status]');
  const labelNode = card.querySelector<HTMLElement>('[data-home-checkin-label]'), noteNode = card.querySelector<HTMLElement>('[data-home-checkin-note]'), meta = card.querySelector<HTMLElement>('[data-home-checkin-meta]');
  if (!trigger || !statusNode || !labelNode || !noteNode) return;
  const button: HTMLButtonElement = trigger, status: HTMLElement = statusNode, label: HTMLElement = labelNode, note: HTMLElement = noteNode;
  const {api,getSession,signIn} = window.ChunnelInteractions;
  const wallet = card.dataset.checkinSource === 'wallet', path = wallet ? '/api/v1/social/wallets/main' : '/api/v1/social/progression';
  let busy = false, serial = 0, identity: string | null = null;
  const walletRequests = new Map<string, CurrencyPending<'check-in'>>(), progressionRequests = new Map<string, ProgressionPending<'check-in'>>();
  const message = (text: string, tone = '') => { status.textContent = text; status.hidden = !text; status.dataset.tone = tone; };
  const state = (text: string, done = false, detail = '') => { label.textContent = text; button.disabled = done; if (detail) note.textContent = detail; };
  const setMeta = (text: string) => { if (meta) meta.textContent = text; };
  const nextText = (at: string) => { const value = window.ChunnelTime?.formatDateTime(at) || ''; return value ? `下次可签：${value}` : ''; };
  const observe = (session: ClientSession) => {
    const next = session.authenticated && session.id ? String(session.id) : '';
    if (identity !== null && identity !== next) {
      serial++; busy = false; button.removeAttribute('aria-busy');
      state('签到领取', false, next ? '核对当前账号的签到状态后领取。' : '登录后即可领取签到奖励。'); setMeta(''); message('');
    }
    identity = next;
  };
  document.addEventListener('chunnel:session', event => observe(event.detail));
  async function claim(): Promise<void> {
    if (busy || button.disabled) return;
    const request = ++serial, current = () => request === serial;
    let owner = '';
    busy = true; button.setAttribute('aria-busy','true'); message('正在确认签到状态…');
    try {
      const session = await getSession(true); if (!current()) return;
      observe(session); if (!current()) return;
      if (!session.authenticated) {
        message('登录后即可领取签到奖励。'); busy = false; button.removeAttribute('aria-busy'); await signIn(claim); return;
      }
      owner = String(session.id); if (!id(owner)) throw new Error('账号状态已变化，请刷新后重试。');
      const raw = await api(path); if (!current()) return;
      const snapshot = wallet ? parseWalletReward(raw, owner) : parseProgressionReward(raw, owner);
      if (!snapshot.enabled) { state('签到暂未开放', true, '站点暂时没有开放签到。'); setMeta('签到功能当前未开启'); message('站点暂未开放签到奖励。'); return; }
      if (snapshot.verification) { message('请先到个人中心验证邮箱，再领取奖励。'); return; }
      const pending = wallet ? walletRequests.get(owner) || readPending('check-in', owner) : progressionRequests.get(owner) || read('check-in', owner);
      if (!snapshot.ready && !pending) { state('本轮已领取', true, '本轮奖励已经入账。'); setMeta(nextText(snapshot.next)); message('本轮签到已经领取。'); return; }
      let payload: {version: string; request_id: string};
      if (wallet) {
        const walletRequest = pending && 'version' in pending ? pending : savePending('check-in', owner, {version:snapshot.version,request_id:nonce()});
        walletRequests.set(owner, walletRequest);
        payload = {version:walletRequest.version,request_id:walletRequest.request_id};
      } else {
        const progressionRequest = pending && 'payload' in pending ? pending : save('check-in', owner, {version:snapshot.version,request_id:nonce()});
        progressionRequests.set(owner, progressionRequest);
        payload = progressionRequest.payload;
      }
      message('正在领取奖励…');
      const rawResult = await api(path+'/check-in', {method:'POST',body:payload}); if (!current()) return;
      const result = snapshot.source === 'wallet' ? walletRewardReceipt(rawResult, snapshot.precision) : progressionRewardReceipt(rawResult, owner);
      // A confirmed write remains confirmed even when the follow-up read fails.
      if (wallet) { clearPending('check-in',owner); walletRequests.delete(owner); } else { clear('check-in',owner); progressionRequests.delete(owner); }
      state('本轮已领取',true,`本轮已领取 ${result.delta} ${snapshot.name}。`);
      message(result.replayed ? '这次签到已领取，未重复发放。' : `签到成功，获得 ${result.delta} ${snapshot.name}。`);
      try { const refreshed=await api(path); if (!current()) return; setMeta(nextText((wallet ? parseWalletReward(refreshed,owner) : parseProgressionReward(refreshed,owner)).next)); }
      catch { if(current())setMeta('下次可签到时间暂未更新，请刷新查看。'); }
      if(current()&&!wallet)document.dispatchEvent(new Event('chunnel:checkin'));
    } catch (problem) {
      if(!current())return;
      const error=failure(problem);
      if(owner&&(wallet?definiteRejection(error):rejected(error))){if(wallet){clearPending('check-in',owner);walletRequests.delete(owner);}else{clear('check-in',owner);progressionRequests.delete(owner);}}
      state('重试签到');message(error.message,'error');
    } finally { if(current()){busy=false;button.removeAttribute('aria-busy');} }
  }
  button.addEventListener('click',claim);
}
mountHomeRewards();
