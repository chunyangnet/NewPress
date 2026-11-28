export const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
export const integer = (value: unknown, min = 0, max = Number.MAX_SAFE_INTEGER): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= min && value <= max;
export const text = (value: unknown, max = Number.MAX_SAFE_INTEGER): value is string => typeof value === 'string' && value.length <= max;
export const digest = (value: unknown): value is string => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
export const date = (value: unknown): value is string => text(value, 64) && /^\d{4}-\d\d-\d\dT/.test(value) && Number.isFinite(Date.parse(value));
export const own = <T extends object>(obj:T, key:unknown):key is keyof T => typeof key === 'string' && Object.prototype.hasOwnProperty.call(obj,key);
export class PluginRequestError extends Error { status = 0; code = ''; confirmedRejection = false; }
export function pluginError(error:unknown) {
 const e = object(error) ? error : {};
 return {name:text(e.name)?e.name:'Error',message:text(e.message)&&e.message?e.message:'连接中断，请核对操作结果后重试。',status:integer(e.status)?e.status:0,code:text(e.code)?e.code:'',confirmedRejection:e.confirmedRejection===true};
}
