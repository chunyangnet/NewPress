export interface VerificationOptions {preview?:string;csrf?:string}
export interface VerificationProof {token?:string;challenge_id?:string;answer?:string;lot_number?:string;captcha_output?:string;pass_token?:string;gen_time?:string;randstr?:string}
export interface VerificationAPI {getToken(owner:HTMLElement,scene:string,options?:VerificationOptions):Promise<string>;cancel(owner:HTMLElement):void}
export interface VerificationTask {promise:Promise<string>;cancel():void}
interface WidgetOptions {
  sitekey:string;theme:string;size:string;callback:(token:string)=>void|Promise<void>;
  action?:string;language?:string;retry?:string;
  'expired-callback':()=>void;'error-callback':()=>void|boolean;
}
interface CaptchaWidget {render(host:HTMLElement,options:WidgetOptions):string|number;remove(id:string|number):void}
interface RecaptchaWidget {render(host:HTMLElement,options:WidgetOptions):number;reset(id:number):void;ready(callback:()=>void):void;execute(key:string,options:{action:string}):Promise<string>}
interface GeetestWidget {
  destroy?():void;showCaptcha():void;getValidate():VerificationProof|null;
  onReady(callback:()=>void):GeetestWidget;onSuccess(callback:()=>void):GeetestWidget;onError(callback:()=>void):GeetestWidget;
}
interface TencentResult {ret:number;ticket:string;randstr:string}
interface TencentWidget {show():void;destroy?():void}
declare global {
  interface Window {
    [key:`__chunnelVerificationReady${number}`]:(()=>void)|undefined;
    turnstile?:CaptchaWidget;hcaptcha?:CaptchaWidget;grecaptcha?:RecaptchaWidget;
    initGeetest4?:(options:{captchaId:string;product:string;language:string},callback:(widget:GeetestWidget)=>void)=>void;
    TencentCaptcha?:new(key:string,callback:(result:TencentResult)=>void,options:{userLanguage:string})=>TencentWidget;
  }
}
