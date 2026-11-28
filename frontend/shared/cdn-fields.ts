export type CDNField=HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement;
export function fieldValue(field:CDNField):string|number|boolean|string[] {
 if(field instanceof HTMLSelectElement&&field.multiple)return [...field.selectedOptions].map(option=>option.value);
 if(field instanceof HTMLInputElement){if(field.type==='checkbox')return field.checked;if(field.type==='number')return field.valueAsNumber;}
 return field.value;
}
export function writeField(target:object,key:unknown,value:unknown):void {
 if(typeof key!=='string'||!Object.hasOwn(target,key)&&key!=='secret'&&key!=='token')return;
 const fields=target as Record<string,unknown>,previous=fields[key];
 if((key==='secret'||key==='token')&&typeof value==='string'||typeof previous===typeof value&&!Array.isArray(previous)||Array.isArray(previous)&&Array.isArray(value)&&value.every(v=>typeof v==='string'))fields[key]=value;
}
