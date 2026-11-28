import {object} from './plugin-data-common';
export const statusError=(error:unknown)=>({status:object(error)&&typeof error.status==='number'?error.status:0});
export interface PollState {enabled:boolean;busy:boolean;suspended:boolean;failures:number;authStatus:number}
interface Flight {controller:AbortController;timeout:number|null;timedOut:boolean}
export interface StatusPollerOptions<T> {setTimer?:(fn:()=>void,delay:number)=>number;clearTimer?:(id:number)=>void;visible?:()=>boolean;load:(signal:AbortSignal)=>Promise<T>;onData:(data:T,manual:boolean)=>void;onError?:(error:unknown,timedOut:boolean)=>void;onState?:(state:PollState)=>void}
  export function createStatusPoller<T>(options:StatusPollerOptions<T>) {
    const setTimer = options.setTimer || setTimeout;
    const clearTimer = options.clearTimer || clearTimeout;
    const visible = options.visible || (() => true);
    let timer:number|null = null;
    let flight:Flight|null = null;
    let enabled = true;
    let suspended = false;
    let destroyed = false;
    let failures = 0;
    let authStatus = 0;

    function notify() {
      options.onState?.({ enabled, busy: !!flight, suspended, failures, authStatus });
    }
    function clear() {
      if (timer !== null) clearTimer(timer);
      timer = null;
    }
    function cancel() {
      const previous = flight;
      flight = null;
      if (previous) {
        if(previous.timeout!==null)clearTimer(previous.timeout);
        previous.controller.abort();
      }
    }
    function schedule(delay = Math.min(60000, 15000 * (1 + failures))) {
      clear();
      if (!destroyed && enabled && !suspended && visible() && !flight) {
        timer = setTimer(() => { timer = null; refresh(); }, delay);
      }
    }
    async function refresh(manual = false) {
      if (destroyed || suspended || !visible() || flight) return;
      clear();
      const current:Flight = { controller: new AbortController(), timeout: null, timedOut: false };
      flight = current;
      notify();
      current.timeout = setTimer(() => {
        current.timedOut = true;
        current.controller.abort();
      }, 10000);
      try {
        const result = await options.load(current.controller.signal);
        if (flight !== current || destroyed || suspended || !visible()) return;
        if (current.timedOut) throw new Error('timeout');
        options.onData(result, manual);
        failures = 0;
        authStatus = 0;
      } catch (error) {
        if (flight !== current || destroyed || suspended || !visible()) return;
        failures++;
        if (statusError(error).status === 401 || statusError(error).status === 403) {
          authStatus = statusError(error).status;
          enabled = false;
        }
        options.onError?.(error, current.timedOut);
      } finally {
        if(current.timeout!==null)clearTimer(current.timeout);
        if (flight === current) {
          flight = null;
          notify();
          schedule();
        }
      }
    }
    return {
      start() { notify(); schedule(2200); },
      refresh,
      setEnabled(next:boolean) {
        enabled = !!next;
        clear();
        if (!enabled) cancel();
        notify();
        if (enabled) refresh();
      },
      suspend() { suspended = true; clear(); cancel(); notify(); },
      resume() { suspended = false; notify(); if (enabled) refresh(); },
      destroy() { destroyed = true; clear(); cancel(); notify(); },
    };
  }

