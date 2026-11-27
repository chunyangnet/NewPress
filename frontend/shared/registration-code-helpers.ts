import type { RegistrationCode, CodeRules } from './registration-admin-data';
type StatusItem = Pick<RegistrationCode, 'enabled' | 'max_uses' | 'used' | 'starts_at' | 'expires_at'>;
  const statuses={active:'可用',scheduled:'未生效',exhausted:'已用完',expired:'已过期',disabled:'已停用'};
  export function statusOf(item: StatusItem,now=Date.now()) {
    if(!item.enabled)return 'disabled';
    if(item.expires_at&&Date.parse(item.expires_at)<=now)return 'expired';
    if(item.max_uses>0&&item.used>=item.max_uses)return 'exhausted';
    if(item.starts_at&&Date.parse(item.starts_at)>now)return 'scheduled';
    return 'active';
  }
  export function csvRows(text: string) {
    const rows: string[][]=[];let row: string[]=[],cell='',quoted=false,closed=false;
    for(let i=0;i<text.length;i++){
      const c=text.charAt(i);
      if(quoted){if(c==='"'){if(text[i+1]==='"'){cell+='"';i++;}else{quoted=false;closed=true;}}else cell+=c;continue;}
      if(c==='"'){if(cell||closed)throw new Error('CSV 引号格式不正确');quoted=true;continue;}
      if(c===','||c==='\n'||c==='\r'){row.push(cell);cell='';closed=false;if(c!==','){if(row.some(v=>v.trim()))rows.push(row);row=[];if(c==='\r'&&text[i+1]==='\n')i++;}continue;}
      if(closed){if(c===' '||c==='\t')continue;throw new Error('CSV 引号后存在多余内容');}cell+=c;
    }
    if(quoted)throw new Error('CSV 引号未闭合');
    row.push(cell);if(row.some(v=>v.trim()))rows.push(row);return rows;
  }
  export function parseImport(text: string,format='auto') {
    text=String(text).replace(/^\uFEFF/,'').trim();if(!text)return [];
    let codes: unknown[];
    if(format==='auto')format=text.startsWith('[')||text.startsWith('{')?'json':/^(?:"注册码"|注册码)(?:,|\r?\n|$)/.test(text)||/^[^\r\n]*,\s*"?注册码"?(?:,|\r?\n|$)/.test(text)?'csv':'text';
    if(format==='json'){
      let value: unknown;try{value=JSON.parse(text);}catch{throw new Error('JSON 格式不正确');}
      const items=Array.isArray(value)?value:(value && typeof value === 'object' ? (value as { codes?: unknown; items?: unknown }).codes || (value as { items?: unknown }).items : undefined);
      if(!Array.isArray(items))throw new Error('JSON 须为注册码数组，或含 codes / items 数组的对象');
      codes=items.map(item=>typeof item==='string'?item:item && typeof item === 'object' ? (item as { code?: unknown }).code : undefined);
    }else if(format==='csv'){
      const rows=csvRows(text),header=rows[0]||[],column=header.findIndex(v=>['注册码','code'].includes(v.trim().toLowerCase()));
      if(column<0)throw new Error('CSV 首行必须包含「注册码」或「code」列名');
      if(header.filter(v=>['注册码','code'].includes(v.trim().toLowerCase())).length!==1)throw new Error('CSV 只能包含一个注册码列');
      codes=rows.slice(1).map(row=>{const value=row[column];return typeof value==='string'&&/^'-[A-Za-z0-9_-]+$/.test(value)?value.slice(1):value;});
    }else if(format==='text')codes=text.split(/[\s,，;；]+/);
    else throw new Error('请选择有效的导入格式');
    if(codes.length>500)throw new Error('每批最多导入 500 个注册码');
    const seen=new Set();return codes.map((value,i)=>{
      if(typeof value!=='string'||!/^[A-Za-z0-9_-]{4,128}$/.test(value.trim()))throw new Error(`第 ${i+1} 个注册码格式不正确`);
      const code=value.trim();if(seen.has(code))throw new Error(`第 ${i+1} 个注册码与前面的内容重复`);seen.add(code);return code;
    });
  }
  export function maxUses(value: unknown) {
    if(String(value).trim()==='')throw new Error('请填写有效总次数，0 表示不限次数');
    const count=Number(value);if(!Number.isInteger(count)||count<0||count>1000000)throw new Error('有效总次数须为 0 至 1000000 的整数');return count;
  }
  export function csvCell(value: unknown) {
    let text=String(value??'');if(/^[\s]*[=+\-@\t\r]/.test(text))text="'"+text;
    return '"'+text.replace(/"/g,'""')+'"';
  }
  export function exportCSV(items: (StatusItem & Pick<RegistrationCode, 'code' | 'note' | 'created_at' | 'last_used_at'>)[],now=Date.now()) {
    const rows=[['注册码','状态','已使用次数','有效总次数（0不限）','生效时间','到期时间','备注','创建时间','最近使用时间'],...items.map(c=>[c.code,statuses[statusOf(c,now)],c.used,c.max_uses,c.starts_at||'',c.expires_at||'',c.note,c.created_at,c.last_used_at||''])];
    return '\uFEFF'+rows.map(row=>row.map(csvCell).join(',')).join('\r\n')+'\r\n';
  }
  export function formatPreview(g: { prefix?: string; length?: string | number; group?: string | number; charset: string }) {
    const alphabet=({friendly:'ABCD2345EFGH6789',mixed:'aB3dE7fG2hJ9kL4m',digits:'2385169470',hex:'A1B2C3D4E5F60789'} as Record<string, string>)[g.charset]||'';
    const length=Math.max(0,Math.min(64,Number(g.length)||0)),group=Number(g.group)||0;
    let random='';for(let i=0;i<length;i++){if(group&&i&&i%group===0)random+='-';random+=alphabet[i%alphabet.length]||'A';}
    return String(g.prefix||'')+random;
  }

export { statuses };
