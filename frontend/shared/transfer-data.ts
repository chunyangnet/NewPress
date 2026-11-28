import {object,integer,text,date} from './plugin-data-common';

export const invalidTransfer = () => new Error('响应不完整或与本次操作不一致，请核对结果。');
export type Decoder<T> = (value:unknown)=>T;
type Shape = Record<string,Decoder<unknown>>;
type Fields<S extends Shape> = {[K in keyof S]:ReturnType<S[K]>};
export const string = (max=16384):Decoder<string> => value => {if(!text(value,max))throw invalidTransfer();return value;};
export const number = (min=0,max=Number.MAX_SAFE_INTEGER):Decoder<number> => value => {if(!integer(value,min,max))throw invalidTransfer();return value;};
export const boolean:Decoder<boolean> = value => {if(typeof value!=='boolean')throw invalidTransfer();return value;};
export const timestamp:Decoder<string> = value => {if(!date(value))throw invalidTransfer();return value;};
export const optional = <T>(decode:Decoder<T>,fallback:T):Decoder<T> => value => value===undefined?fallback:decode(value);
export const choice = <const T extends readonly string[]>(...values:T):Decoder<T[number]> => value => {if(typeof value!=='string'||!values.includes(value))throw invalidTransfer();return value as T[number];};
export const array = <T>(decode:Decoder<T>,max=1000):Decoder<T[]> => value => {if(value===null)return [];if(!Array.isArray(value)||value.length>max)throw invalidTransfer();return value.map(decode);};
export const map = <T>(decode:Decoder<T>,max=1000):Decoder<Record<string,T>> => value => {if(value===null)return {};if(!object(value)||Object.keys(value).length>max)throw invalidTransfer();return Object.fromEntries(Object.entries(value).map(([key,item])=>[key,decode(item)]));};
export const shape = <S extends Shape>(fields:S):Decoder<Fields<S>> => value => {
 if(!object(value))throw invalidTransfer();
 return Object.fromEntries(Object.entries(fields).map(([key,decode])=>[key,decode(value[key])])) as Fields<S>;
};
export const unique = <T>(items:T[],key:(item:T)=>unknown):T[] => {if(new Set(items.map(key)).size!==items.length)throw invalidTransfer();return items;};
export const hash:Decoder<string> = value => {if(!text(value)||!/^[a-f0-9]{64}$/.test(value))throw invalidTransfer();return value;};
export function equal(a:unknown,b:unknown):boolean {
 if(a===b)return true;
 if(Array.isArray(a)&&Array.isArray(b))return a.length===b.length&&a.every((v,i)=>equal(v,b[i]));
 if(object(a)&&object(b)){const keys=Object.keys(a);return keys.length===Object.keys(b).length&&keys.every(k=>Object.hasOwn(b,k)&&equal(a[k],b[k]));}
 return false;
}
export function ok(value:unknown):Record<string,unknown> {if(!object(value)||value.ok!==true)throw invalidTransfer();return value;}
