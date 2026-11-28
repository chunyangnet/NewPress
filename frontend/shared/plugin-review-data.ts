import {object,integer,text,digest,date,own} from './plugin-data-common';
export const faults = {invalid_data: '原记录无法完整校验', index_mismatch: '记录与索引不一致', duplicate_identity: '逻辑身份重复'};
export const dispositions = {receipt_preserved: '保留原内容结果', authority_retired: '授权已退休', duplicate_preserved: '保留相同内容回执', duplicate_authority_retired: '保留已退休的授权', webhook_cancelled: '已取消待发送意图', webhook_uncertain: '已领取发送保留为待核对', webhook_preserved: '保留原发送结果', duplicate_webhook_preserved: '保留相同且不会重发的操作'};
export const webhookStates = {pending: '等待源任务成功', sending: '发送已领取', cancelled: '发送前已取消', uncertain: '待人工核对', http_confirmed: 'HTTP 已确认', reviewed_received: '人工确认已收到', reviewed_not_received: '人工确认未收到'};
export const webhookCodes = {http_2xx: '已收到完整 2xx 响应', http_status: '非 2xx 响应，业务结果待核对', transport_unconfirmed: '未取得可确认响应', response_unconfirmed: '响应不完整或超限', worker_expired: '领取到期，结果无法确认', restored: '数据库恢复后停止旧操作', maintenance: '离线维护已停止旧操作', source_cancelled: '源任务已取消', authority_changed: '发送前授权已变化'};
export const states = {pending: '等待源任务成功', sending: '发送已领取', http_confirmed: 'HTTP 已确认', uncertain: '待人工核对', cancelled: '发送前已取消', reviewed_received: '人工确认已收到', reviewed_not_received: '人工确认未收到'};
export const codes = {http_2xx: '已收到完整 2xx 响应', http_status: '已收到非 2xx 响应，业务结果待核对', transport_unconfirmed: '未取得可确认响应', response_unconfirmed: '响应未完整读取或超过上限', worker_expired: '发送领取到期，结果无法确认', restored: '数据库恢复后保留原操作事实', maintenance: '离线维护已停止旧操作，保留原发送事实', source_cancelled: '源任务已取消', authority_changed: '发送前授权已变化'};

export interface WebhookOutcome {code?:keyof typeof codes; http_status?:number; response_bytes?:number; response_digest?:string}
export interface WebhookItem {id:string;job_id:string;fingerprint:string;plugin:string;slot:string;state:keyof typeof states;version:number;created_at:string;updated_at:string;target:string;outcome:WebhookOutcome;result_at?:string;review?:{decision:'received'|'not_received';reason:string;actor_id:string;at:string}}
export interface WebhookQuery {plugin:string;job:string;state:string;limit:number}
export interface WebhookPage {items:WebhookItem[];total:number;limit:number;offset:number}
export interface RecordQuery {scope:string;type:string;plugin:string;job:string;limit:number}
export interface SharedRecord {type:'authority'|'content_effect'|'webhook_intent';reference:string;bytes:number;id?:string;fault?:keyof typeof faults;captured_at?:string;operator?:string;reason?:string;plugin?:string;job_id?:string;index_mismatch?:boolean;revision?:number|bigint;enabled?:boolean;retired?:boolean;
 repair?:{at:string;operator:string;reason:string;disposition:keyof typeof dispositions;output_sha256:string;input_sha256?:string;kept_reference?:string};
 result?:{operation_id:string;post_id:number|bigint;previous_version:number|bigint;version:number|bigint;changed:boolean;committed_at:string};
 webhook?:{operation_id:string;state:keyof typeof webhookStates;version:number;created_at:string;updated_at:string;result_at?:string;outcome:WebhookOutcome};
}
export interface RecordPage {items:SharedRecord[];total:number;next_offset:number;fatal?:'invalid_quarantine'|'storage_capacity'}
const isObject=object;
const domainInteger=(value:unknown):value is number|bigint=>integer(value,1)||typeof value==='bigint'&&value>0n&&value<=9223372036854775807n;
const hasFault=(value:unknown):value is keyof typeof faults=>own(faults,value);

  export function validRecord(item:unknown, scope:string):item is SharedRecord {
    if (!isObject(item) || (!text(item.type) || !['authority', 'content_effect', 'webhook_intent'].includes(item.type)) || !digest(item.reference) || !integer(item.bytes, 0, 144 * 1024)) return false;
    if (scope === 'quarantines') {
      if (!digest(item.id) || !hasFault(item.fault) || !date(item.captured_at) || !text(item.operator, 128) || !item.operator || !text(item.reason, 1024) || !item.reason) return false;
      if (item.repair === undefined) return true;
      const repair = item.repair;
      if (!isObject(repair) || !date(repair.at) || Date.parse(repair.at) < Date.parse(item.captured_at) || !text(repair.operator, 128) || !repair.operator || !text(repair.reason, 1024) || !repair.reason || !digest(repair.output_sha256)) return false;
      const allowed = item.type === 'authority' ? ['authority_retired', 'duplicate_authority_retired'] : item.type === 'webhook_intent' ? ['webhook_cancelled', 'webhook_uncertain', 'webhook_preserved', 'duplicate_webhook_preserved'] : ['receipt_preserved', 'duplicate_preserved'];
      if ((!text(repair.disposition) || !allowed.includes(repair.disposition))) return false;
      if (repair.input_sha256 !== undefined && !digest(repair.input_sha256)) return false;
      const duplicate = repair.disposition.startsWith('duplicate_');
      return duplicate ? digest(repair.kept_reference) && repair.kept_reference === repair.output_sha256 && repair.input_sha256 === undefined : repair.kept_reference === undefined;
    }
    if (item.fault !== undefined && !hasFault(item.fault)) return false;
    if (item.index_mismatch !== undefined && typeof item.index_mismatch !== 'boolean') return false;
    if (item.plugin !== undefined && (!text(item.plugin, 64) || !/^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$/.test(item.plugin))) return false;
    if (item.type === 'authority') {
      if (item.result !== undefined || item.job_id !== undefined || item.webhook !== undefined) return false;
      if (item.revision !== undefined && !domainInteger(item.revision)) return false;
      if (['enabled', 'retired'].some(key => item[key] !== undefined && typeof item[key] !== 'boolean')) return false;
      return item.fault === 'invalid_data' || !!item.plugin && domainInteger(item.revision) && !(item.enabled && item.retired);
    }
    if (item.revision !== undefined || item.enabled !== undefined || item.retired !== undefined) return false;
    if (item.job_id !== undefined && !digest(item.job_id)) return false;
    if (item.type === 'webhook_intent') {
      if (item.result !== undefined) return false;
      if (item.webhook === undefined) return item.fault === 'invalid_data';
      const w = item.webhook, versions = {pending: 1, sending: 2, cancelled: 2, uncertain: 3, http_confirmed: 3, reviewed_received: 4, reviewed_not_received: 4};
      if (!item.plugin || !digest(item.job_id) || !isObject(w) || !digest(w.operation_id) || !own(versions, w.state) || w.version !== versions[w.state] || !date(w.created_at) || !date(w.updated_at) || Date.parse(w.updated_at) < Date.parse(w.created_at) || !isObject(w.outcome)) return false;
      const active = ['pending', 'sending'].includes(w.state), o = w.outcome;
      if (active ? Object.keys(o).length !== 0 : !own(webhookCodes, o.code) || (w.state === 'http_confirmed') !== (o.code === 'http_2xx')) return false;
      if (['pending', 'sending', 'cancelled'].includes(w.state) ? w.result_at !== undefined : !date(w.result_at) || Date.parse(w.result_at) < Date.parse(w.created_at) || Date.parse(w.result_at) > Date.parse(w.updated_at)) return false;
      return !(o.http_status !== undefined && !integer(o.http_status, 100, 599) || o.response_bytes !== undefined && !integer(o.response_bytes, 0, 32768) || o.response_digest !== undefined && !digest(o.response_digest));
    }
    if (item.webhook !== undefined) return false;
    if (item.result === undefined) return item.fault === 'invalid_data';
    const result = item.result;
    return !!item.plugin && digest(item.job_id) && isObject(result) && digest(result.operation_id) && domainInteger(result.post_id) && domainInteger(result.previous_version) && domainInteger(result.version) && typeof result.changed === 'boolean' && BigInt(result.version) === BigInt(result.previous_version) + (result.changed ? 1n : 0n) && date(result.committed_at);
  }
  export function validRecordPage(data:unknown, query:RecordQuery, start:number):data is RecordPage {
    if (!isObject(data) || !Array.isArray(data.items) || data.items.length > query.limit || !integer(data.total, 0, 130000) || !integer(data.next_offset, -1, 130000)) return false;
    if (data.fatal !== undefined) return query.scope === 'records' && text(data.fatal) && ['invalid_quarantine', 'storage_capacity'].includes(data.fatal) && data.items.length === 0 && data.total === 0 && data.next_offset === -1;
    const expectedCount = Math.min(query.limit, Math.max(0, data.total - start));
    if (data.items.length !== expectedCount || data.next_offset !== (start + expectedCount < data.total ? start + expectedCount : -1)) return false;
    const seen = new Set<string>();
    return data.items.every(item => {
      if (!validRecord(item, query.scope) || query.type && item.type !== query.type) return false;
      if (query.scope === 'records' && (query.plugin && item.plugin !== query.plugin || query.job && item.job_id !== query.job)) return false;
      const key = query.scope === 'records' ? item.reference : item.id;
      if (!key || seen.has(key)) return false;
      seen.add(key); return true;
    });
  }

  export function validWebhook(i:unknown):i is WebhookItem {
    if (!object(i) || !digest(i.id) || !digest(i.job_id) || !digest(i.fingerprint) || typeof i.plugin !== 'string' || !/^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$/.test(i.plugin) || typeof i.slot !== 'string' || !/^[A-Za-z][A-Za-z0-9_.-]{0,63}$/.test(i.slot) || !own(states, i.state) || !integer(i.version, 1, 4) || !date(i.created_at) || !date(i.updated_at) || Date.parse(i.updated_at) < Date.parse(i.created_at)) return false;
    if (typeof i.target !== 'string' || i.target.length > 2048 || !/^https:\/\/[a-z0-9.-]+$/.test(i.target) || !object(i.outcome)) return false;
    const o = i.outcome, version = {pending: 1, sending: 2, cancelled: 2, uncertain: 3, http_confirmed: 3, reviewed_received: 4, reviewed_not_received: 4};
    if (i.version !== version[i.state]) return false;
    if (['pending', 'sending', 'cancelled'].includes(i.state)) { if (i.result_at !== undefined) return false; }
    else if (!date(i.result_at) || Date.parse(i.result_at) > Date.parse(i.updated_at)) return false;
    if (['pending', 'sending'].includes(i.state)) { if (Object.keys(o).length) return false; }
    else if (!own(codes, o.code) || (i.state === 'http_confirmed') !== (o.code === 'http_2xx')) return false;
    if (o.http_status !== undefined && !integer(o.http_status, 100, 599) || o.response_bytes !== undefined && !integer(o.response_bytes, 0, 32768) || o.response_digest !== undefined && !digest(o.response_digest)) return false;
    if (i.state.startsWith('reviewed_')) return object(i.review) && i.state === 'reviewed_' + i.review.decision && typeof i.review.reason === 'string' && i.review.reason.length <= 2000 && i.review.reason.trim() !== '' && typeof i.review.actor_id === 'string' && /^[1-9][0-9]{0,18}$/.test(i.review.actor_id) && BigInt(i.review.actor_id) <= 9223372036854775807n && date(i.review.at) && i.review.at === i.updated_at;
    return i.review === undefined;
  }
  export function validWebhookPage(data:unknown, q:WebhookQuery, start:number):data is WebhookPage {
    if (!object(data) || !Array.isArray(data.items) || !integer(data.total, 0, 10000) || data.limit !== q.limit || data.offset !== start || data.items.length !== Math.min(q.limit, Math.max(0, data.total - start))) return false;
    const ids = new Set<string>();
    return data.items.every(i => { if (!validWebhook(i) || ids.has(i.id) || q.plugin && i.plugin !== q.plugin || q.job && i.job_id !== q.job || q.state && i.state !== q.state) return false; ids.add(i.id); return true; });
  }

export function parseReviewJSON(raw:string):unknown {
        const normalized = JSON.stringify(JSON.parse(raw));
        let marker = '__review_integer_';
        while (normalized.includes(marker)) marker += '_';
        const integers:bigint[] = [];
        return JSON.parse(raw.replace(/"(?:[^"\\]|\\.)*"|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g, (token:string, number:string|undefined) =>
          number && /^-?\d+$/.test(number) && !Number.isSafeInteger(Number(number)) ? JSON.stringify(marker + (integers.push(BigInt(number)) - 1)) : token),
          (_:string, value:unknown) => typeof value === 'string' && value.startsWith(marker) ? integers[Number(value.slice(marker.length))] : value);

}
