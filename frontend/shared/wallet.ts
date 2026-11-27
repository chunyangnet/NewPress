import type { WalletSnapshot, CurrencyEntry, TransferDraft } from './currency-types';
import {required, input} from './account-dom';
import {failure} from './ledger-data';
import {parseWallet, parseCurrencies, parseRecipient, parseCurrencyReceipt} from './wallet-data';
export function mountWallet(): void {
  const element = document.querySelector<HTMLElement>('[data-currency-wallet]');
  if (!element || !window.ChunnelInteractions || !window.ChunnelCurrency) return;
  const root: HTMLElement = element;
  const owner = required<HTMLElement>(document, '[data-account-room]').dataset.accountUser || '';
  const { api, getSession } = window.ChunnelInteractions;
  const C = window.ChunnelCurrency;
  if (!C.id(owner)) return;
  const $ = <T extends Element = HTMLElement>(selector: string): T => required<T>(root, selector);
  const form = $<HTMLFormElement>('[data-wallet-transfer]'), dialog = $<HTMLDialogElement>('[data-wallet-confirm]');
  const result = $('[data-wallet-transfer-result]'), message = $('[data-wallet-message]');
  const select = $<HTMLSelectElement>('[data-wallet-currency]');
  let currency = new URLSearchParams(window.location?.search || '').get('currency') || 'main', catalogLoaded = false, currencies = new Set<string>();
  const path = (key = currency) => '/api/v1/social/wallets/' + encodeURIComponent(key);
  let wallet: WalletSnapshot | null = null, serial = 0, epoch = 0, lostSession = false, synced = false;
  let entries: CurrencyEntry[] = [], next = '', draft: TransferDraft | null = null, sending = false, checking = false, resolving = false, paging = false;
  let pending = C.readPending('transfer', owner, currency), checkIn = C.readPending('check-in', owner, currency);
  const transferRequests = new Map<string, typeof pending>(), checkInRequests = new Map<string, typeof checkIn>();
  const text = (selector: string, value: string) => { $(selector).textContent = value; };

  function controls() {
    select.disabled = lostSession || !catalogLoaded || sending || checking || resolving;
    required<HTMLFieldSetElement>(form, 'fieldset').disabled = lostSession || !wallet || ((!wallet.settings.enabled || !wallet.settings.allow_transfers) && !pending);
    input(form, 'recipient').readOnly = !!pending || sending;
    input(form, 'amount').readOnly = !!pending || sending;
    $<HTMLButtonElement>('[data-wallet-transfer-submit]').disabled = sending || resolving;
    $<HTMLButtonElement>('[data-wallet-transfer-submit]').textContent = pending ? '重试原转账，确认结果' : '核对转账 →';
    $<HTMLButtonElement>('[data-wallet-check-in]').disabled = lostSession || checking || !wallet || (!wallet.check_in_ready && !checkIn);
    $<HTMLButtonElement>('[data-wallet-check-in]').textContent = checking ? '正在领取…' : checkIn ? '重试领取' : wallet?.check_in_ready ? '签到领取' : '暂不可领取';
    $<HTMLButtonElement>('[data-wallet-confirm-send]').disabled = sending;
    $<HTMLButtonElement>('[data-wallet-cancel]').disabled = sending;
    $<HTMLButtonElement>('[data-wallet-more]').disabled = lostSession || paging || !wallet || !next;
    if (pending) { input(form, 'recipient').value = pending.recipient; input(form, 'amount').value = pending.amount; }
  }
  function pendingNotice() {
    if (pending) C.message(result, '上次转账结果尚未确认。收款账号与金额已保留，请重试原请求，避免重复转账。');
  }
  const validateWallet = (data: unknown) => parseWallet(data, owner, currency);
  function installCurrencies(data: unknown) {
    const choices = parseCurrencies(data), seen = new Set(choices.map(item => item.key));
    const options = choices.map(item => { const option = document.createElement('option'); option.value = item.key; option.textContent = item.name + ' · ' + item.key + (item.enabled ? '' : '（已关闭）'); return option; });
    select.replaceChildren(...options); select.value = currency; currencies = seen; catalogLoaded = true;
    if (!seen.has(currency)) throw new Error('所选货币不存在，请从列表重新选择');
  }
  function clearDisplay() {
    for (const selector of ['[data-wallet-balance]', '[data-wallet-transferred]', '[data-wallet-rewarded]', '[data-wallet-remaining]']) text(selector, '—');
    text('[data-wallet-name]', '我的钱包'); text('[data-wallet-symbol]', '');
    text('[data-wallet-confirm-amount]', ''); text('[data-wallet-confirm-recipient]', ''); text('[data-wallet-confirm-status]', '');
    $('[data-wallet-history]').replaceChildren(); $<HTMLButtonElement>('[data-wallet-more]').hidden = true;
  }
  select.addEventListener('change', () => {
    const nextCurrency = select.value;
    if (sending || checking || resolving || lostSession || !currencies.has(nextCurrency) || nextCurrency === currency) { select.value = currency; return; }
    // sessionStorage may be blocked. Preserve each currency's uncertain operation in memory too.
    transferRequests.set(currency, pending); checkInRequests.set(currency, checkIn);
    currency = nextCurrency; epoch++; serial++; synced = false; wallet = null; entries = []; next = ''; draft = null; paging = false;
    pending = transferRequests.get(currency) || C.readPending('transfer', owner, currency); checkIn = checkInRequests.get(currency) || C.readPending('check-in', owner, currency);
    dialog.close(); form.reset(); clearDisplay(); C.message(result, ''); C.message(message, '');
    if (window.location?.href && window.history?.replaceState) {
      const url = new URL(window.location.href); url.searchParams.set('currency', currency);
      window.history.replaceState(window.history.state, '', url.pathname + url.search + url.hash);
    }
    controls(); load();
  });
  function paint(data: WalletSnapshot) {
    const cfg = data.settings;
    text('[data-wallet-currency-caption]', currency + ' · 各币种独立记账');
    text('[data-wallet-name]', cfg.name); text('[data-wallet-symbol]', cfg.symbol); text('[data-wallet-balance]', data.balance);
    text('[data-wallet-state]', cfg.enabled ? '账户正常 · ' + cfg.name : '虚拟币当前已关闭');
    text('[data-wallet-transferred]', data.transferred_today); text('[data-wallet-rewarded]', data.rewarded_today); text('[data-wallet-remaining]', data.transfer_remaining);
    for (const [kind, amount] of [['check-in', cfg.check_in_reward], ['registration', cfg.registration_reward], ['publication', cfg.publication_reward]]) text(`[data-wallet-${kind}-reward]`, '+' + amount);
    const nextAt = data.next_check_in_at ? new Date(data.next_check_in_at) : null;
    text('[data-wallet-check-in-description]', !cfg.check_in_enabled ? '站点尚未开启签到奖励。' : nextAt && nextAt.getTime() > Date.now() ? '下次可领：' + (window.ChunnelTime?.formatDateTime(data.next_check_in_at || '') || '') : `每次 ${cfg.check_in_reward} ${cfg.symbol}，${cfg.check_in_mode === 'calendar_day' ? `每天零点重置（${cfg.check_in_timezone === 'Asia/Shanghai' ? '北京时间' : cfg.check_in_timezone}）` : `间隔 ${cfg.check_in_interval_hours} 小时`}。`);
    text('[data-wallet-registration-description]', `首次开启后注册的新账号，奖励 ${cfg.registration_reward} ${cfg.symbol}，每人一次。`);
    text('[data-wallet-publication-description]', `新文章公开后，正文至少 ${cfg.publication_min_length} 字；24 小时最多 ${cfg.publication_daily_limit} 篇。`);
    text('[data-wallet-registration-state]', cfg.enabled && cfg.registration_enabled ? '符合规则后自动发放' : '尚未开启');
    text('[data-wallet-publication-state]', cfg.enabled && cfg.publication_enabled ? '符合规则后自动发放' : '尚未开启');
    text('[data-wallet-reward-help]', data.verification_required ? '当前账号的邮箱尚未验证，暂不能领取奖励。' : `24 小时奖励上限 ${cfg.daily_reward_limit} ${cfg.symbol}，剩余 ${data.reward_remaining} ${cfg.symbol}。`);
    text('[data-wallet-transfer-rule]', cfg.enabled && cfg.allow_transfers ? `每笔 ${cfg.min_transfer}–${cfg.max_transfer} ${cfg.symbol}，24 小时最多转出 ${cfg.daily_transfer_limit} ${cfg.symbol}。` : '站点当前未开放转账，已有收支记录仍可查看。');
    text('[data-wallet-transfer-unit]', cfg.symbol);
    C.history($('[data-wallet-history]'), entries);
    $<HTMLButtonElement>('[data-wallet-more]').hidden = !next;
    controls(); pendingNotice();
  }

  async function load() {
    const request = ++serial, activeEpoch = epoch, activeCurrency = currency;
    if (lostSession) return;
    wallet = null; paging = false; controls(); text('[data-wallet-state]', '正在刷新…'); $<HTMLButtonElement>('[data-wallet-refresh]').disabled = true;
    let syncWarning = '';
    try {
      const session = await getSession();
      if (!session?.authenticated || String(session.id) !== owner) { loseSession(); return; }
      if (request !== serial || activeEpoch !== epoch || lostSession) return;
      if (!catalogLoaded) {
        const choices = await api('/api/v1/social/wallets');
        if (request !== serial || activeEpoch !== epoch || lostSession) return;
        installCurrencies(choices); controls();
      }
      if (!currencies.has(activeCurrency)) throw new Error('所选货币不存在，请从列表重新选择');
      if (!synced) {
        synced = true;
        try {
          const data = await api(path(activeCurrency) + '/sync', { method: 'POST', body: {} });
          if (data.currency_key !== activeCurrency) throw new Error('奖励货币未确认');
        }
        catch (problem) { const error = failure(problem); if (error.name === 'AbortError') return; if (activeEpoch === epoch) synced = false; syncWarning = '自动奖励同步暂未完成，可刷新钱包重试。'; }
      }
      if (request !== serial || activeEpoch !== epoch || lostSession) return;
      const response = await api(path(activeCurrency));
      if (request !== serial || activeEpoch !== epoch || lostSession) return;
      const data = validateWallet(response);
      wallet = data; entries = data.entries; next = data.next_before;
      C.message(message, syncWarning); paint(data);
    } catch (problem) { const error = failure(problem);
      if (request !== serial || activeEpoch !== epoch || error.name === 'AbortError') return;
      C.message(message, error.message || '钱包暂时无法载入', 'error'); text('[data-wallet-state]', '读取失败，请刷新重试');
    } finally { if (request === serial) { $<HTMLButtonElement>('[data-wallet-refresh]').disabled = false; controls(); } }
  }
  $<HTMLButtonElement>('[data-wallet-refresh]').addEventListener('click', () => { synced = false; catalogLoaded = false; load(); });
  $<HTMLButtonElement>('[data-wallet-more]').addEventListener('click', async () => {
    if (!next || !wallet || lostSession || paging) return;
    const button = $<HTMLButtonElement>('[data-wallet-more]'), request = serial, activeEpoch = epoch, activeCurrency = currency, before = next;
    paging = true; controls();
    try {
      const response = await api(path(activeCurrency) + '?' + new URLSearchParams({ before }));
      if (request !== serial || activeEpoch !== epoch || lostSession) return;
      const data = parseWallet(response, owner, activeCurrency);
      const seen = new Set(entries.map(entry => entry.id)); entries.push(...data.entries.filter(entry => !seen.has(entry.id)));
      next = data.next_before; C.history($('[data-wallet-history]'), entries); button.hidden = !next;
    } catch (problem) { const error = failure(problem); if (request === serial && activeEpoch === epoch && !lostSession && error.name !== 'AbortError') C.message(message, error.message, 'error'); }
    finally { if (request === serial && activeEpoch === epoch) { paging = false; controls(); } }
  });

  function confirm(value: TransferDraft) {
    draft = value;
    text('[data-wallet-confirm-amount]', value.amount + ' ' + (value.symbol || wallet?.settings.symbol || ''));
    text('[data-wallet-confirm-recipient]', '@' + value.recipient);
    text('[data-wallet-confirm-status]', pending ? '这将重试同一笔转账，不会重复扣款。' : '');
    $<HTMLButtonElement>('[data-wallet-confirm-send]').textContent = pending ? '重试原转账' : '确认转账';
    dialog.showModal();
  }
  form.addEventListener('submit', async event => {
    event.preventDefault(); if (!wallet || sending || resolving || lostSession) return;
    if (pending) { confirm(pending); return; }
    const currentWallet = wallet, cfg = wallet.settings, activeEpoch = epoch;
    resolving = true; controls();
    try {
      const amount = C.units(input(form, 'amount').value.trim(), cfg.precision);
      if (amount <= 0n) throw new Error('转账金额必须大于零');
      if (amount < C.units(cfg.min_transfer, cfg.precision) || amount > C.units(cfg.max_transfer, cfg.precision)) throw new Error('金额不在单笔转账范围内');
      if (amount > C.units(wallet.balance, cfg.precision)) throw new Error('余额不足');
      if (amount > C.units(wallet.transfer_remaining, cfg.precision)) throw new Error('金额超过剩余转出额度');
      const recipient = parseRecipient(await api(path() + '/recipient?' + new URLSearchParams({ username: input(form, 'recipient').value.trim() })), currency);
      if (activeEpoch !== epoch || root.hidden || wallet !== currentWallet) return;
      confirm({ currency_key: currency, recipient_id: recipient.id, recipient: recipient.username, amount: C.format(amount, cfg.precision), version: cfg.version, precision: cfg.precision, symbol: cfg.symbol });
    } catch (problem) { const error = failure(problem); if (activeEpoch === epoch && error.name !== 'AbortError') C.message(result, error.message, 'error'); }
    finally { resolving = false; controls(); }
  });
  $<HTMLButtonElement>('[data-wallet-cancel]').addEventListener('click', () => { if (!sending) { dialog.close(); draft = null; } });
  dialog.addEventListener('cancel', event => { if (sending) event.preventDefault(); });
  $<HTMLButtonElement>('[data-wallet-confirm-send]').addEventListener('click', async () => {
    if (!draft || sending || lostSession) return;
    const activeEpoch = epoch;
    try { if (!pending) pending = C.savePending('transfer', owner, { ...draft, request_id: C.nonce() }, currency); }
    catch (problem) { const error = failure(problem); text('[data-wallet-confirm-status]', error.message); return; }
    sending = true; controls(); text('[data-wallet-confirm-status]', '正在确认转账…');
    try {
      const original = pending; if (!original) return;
      const data = parseCurrencyReceipt(await api(path() + '/transfer', { method: 'POST', body: { recipient_id: original.recipient_id, amount: original.amount, version: original.version, request_id: original.request_id } }), currency, original.precision);
      if (activeEpoch !== epoch) return;
      if (data.recipient_id !== original.recipient_id || data.entry.kind !== 'transfer_out' || C.units(data.entry.delta, original.precision, true) !== -C.units(original.amount, original.precision)) throw new Error('转账回执未确认，请重试原请求');
      const sent = original;
      C.clearPending('transfer', owner, currency); transferRequests.delete(currency); pending = null; draft = null; form.reset(); dialog.close();
      C.message(result, `${data.replayed ? '已确认' : '已完成'}：向 @${sent.recipient} 转出 ${sent.amount} ${sent.symbol || ''}，流水 #${data.entry.id}。`);
      await load();
    } catch (problem) { const error = failure(problem);
      if (activeEpoch !== epoch || error.name === 'AbortError') return;
      if (C.definiteRejection(error)) { C.clearPending('transfer', owner, currency); transferRequests.delete(currency); pending = null; draft = null; dialog.close(); await load(); }
      const notice = error.message + (pending ? ' 请重试原转账确认结果，当前账号和金额已保留。' : '');
      C.message(result, notice, 'error'); text('[data-wallet-confirm-status]', notice);
    } finally { sending = false; controls(); }
  });

  $<HTMLButtonElement>('[data-wallet-check-in]').addEventListener('click', async () => {
    if (!wallet || checking || lostSession || (!wallet.check_in_ready && !checkIn)) return;
    const activeEpoch = epoch, precision = wallet.settings.precision;
    try { if (!checkIn) checkIn = C.savePending('check-in', owner, { version: wallet.settings.version, request_id: C.nonce() }, currency); }
    catch (problem) { const error = failure(problem); C.message(message, error.message, 'error'); return; }
    checking = true; controls();
    try {
      const original = checkIn; if (!original) return;
      const data = parseCurrencyReceipt(await api(path() + '/check-in', { method: 'POST', body: { version: original.version, request_id: original.request_id } }), currency, precision);
      if (activeEpoch !== epoch) return;
      if (data.entry.kind !== 'reward_check_in' || C.units(data.entry.delta, precision) <= 0n) throw new Error('签到回执未确认，请重试');
      C.clearPending('check-in', owner, currency); checkInRequests.delete(currency); checkIn = null;
      await load(); if (activeEpoch === epoch) C.message(message, `${data.replayed ? '本轮签到已领取' : '签到成功'}，奖励 ${data.entry.delta} ${wallet?.settings.symbol || ''}。`);
    } catch (problem) { const error = failure(problem);
      if (activeEpoch !== epoch || error.name === 'AbortError') return;
      if (C.definiteRejection(error)) { C.clearPending('check-in', owner, currency); checkInRequests.delete(currency); checkIn = null; await load(); }
      C.message(message, error.message, 'error');
    } finally { checking = false; controls(); }
  });

  function loseSession() {
    if (lostSession) return;
    epoch++; serial++; lostSession = true; wallet = null; entries = []; next = ''; draft = null; paging = false;
    pending = null; checkIn = null; dialog.close(); form.reset();
    transferRequests.clear(); checkInRequests.clear();
    clearDisplay(); C.message(result, ''); C.message(message, '登录账号已变化，请重新打开钱包。'); controls();
  }
  document.addEventListener('chunnel:session', event => {
    if (event.detail?.authenticated === true && String(event.detail.id || '') === owner) return;
    loseSession();
  });
  window.ChunnelWallet = Object.freeze({ load });
}
mountWallet();
