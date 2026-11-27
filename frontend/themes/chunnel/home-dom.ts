export const $ = <T extends Element = HTMLElement>(selector: string): T | null => document.querySelector<T>(selector);
export const $$ = <T extends Element = HTMLElement>(selector: string): NodeListOf<T> => document.querySelectorAll<T>(selector);
