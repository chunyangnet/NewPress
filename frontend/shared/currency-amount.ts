import {maximum} from './ledger-data';
export function units(raw: unknown, precision: number, signed = false): bigint {
  if (typeof raw !== 'string' || !Number.isInteger(precision) || precision < 0 || precision > 6) throw new Error('金额格式无效');
  const negative = signed && raw.startsWith('-'), value = negative ? raw.slice(1) : raw;
  if (!/^(0|[1-9][0-9]*)(\.[0-9]+)?$/.test(value) || value.length > 27) throw new Error('请使用普通十进制金额');
  const [whole = '', fraction = ''] = value.split('.');
  if (fraction.length > precision || value.includes('.') && precision === 0) throw new Error(`金额最多保留 ${precision} 位小数`);
  const amount = BigInt(whole) * 10n ** BigInt(precision) + BigInt(fraction.padEnd(precision, '0') || '0');
  if (amount > maximum) throw new Error('金额超过允许范围');
  return negative ? -amount : amount;
}
export function format(amount: bigint, precision: number): string {
  if (!Number.isInteger(precision) || precision < 0 || precision > 6 || amount > maximum || amount < -maximum) throw new Error('金额格式无效');
  const negative = amount < 0n;
  let text = (negative ? -amount : amount).toString().padStart(precision + 1, '0');
  if (precision) text = text.slice(0, -precision) + '.' + text.slice(-precision);
  return (negative ? '-' : '') + text;
}
