const nodeTest = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const postcss = require('postcss');
const { compile, loadModule } = require('./helpers/typescript.cjs');
const {syncPreviewColorMode} = loadModule('frontend/core/appearance-preview-support.ts');

for (const entry of ['TypeScript', 'generated']) {
  const test = (name, fn) => nodeTest(`${entry}: ${name}`, fn);
  const source = entry === 'TypeScript' ? compile('frontend/shared/color-mode.ts') : fs.readFileSync('themes/chunnel/assets/js/chunnel-color-mode.js', 'utf8');
  const reveal = entry === 'TypeScript' ? compile('frontend/shared/color-reveal.ts') : fs.readFileSync('themes/chunnel/assets/js/chunnel-color-reveal.js', 'utf8');
  class Element {}

  function fixture({saved = null, dark = false, blocked = false, ready = 'loading', viewTransitions = false, reducedMotion = false, siteReduced = false, animationSupport = true, animationLoaded = true, narrow = false, mediaSupport = true, legacyMedia = false, entryURL = '/assets/js/chunnel-color-reveal.js', metadata = true} = {}) {
    const emitter = () => ({ listeners: {}, addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }, dispatchEvent(event) { for (const fn of this.listeners[event.type] || []) fn(event); } });
    const buttons = ['light','dark'].map((mode,index)=>Object.assign(new Element(),{dataset:{colorMode:mode},attributes:{},setAttribute(k,v){this.attributes[k]=v;},closest(){return this;},focus(){this.focused=true;document.activeElement=this;},getBoundingClientRect(){return {left:10+index*42,top:20,width:40,height:40};}}));
    const control = {...emitter(), hidden:true, dataset:{colorModeControl:entryURL}, querySelectorAll:()=>buttons, contains:target=>buttons.includes(target)};
    const meta = {content:'#f7f9fc'};
    const animations = [], scripts = [], transitions = [];
    const root = {dataset:{},animate(frames,timing){const a={frames,timing,cancel(){this.cancelled=true;}};animations.push(a);return a;}};
    const document = {...emitter(), readyState:ready, documentElement:root, body:{matches:()=>siteReduced}, querySelector:()=>metadata?meta:null, querySelectorAll:()=>ready==='loading'?[]:[control],
      createElement:()=>({remove(){}}),head:{append:el=>scripts.push(el)},getAnimations:()=>[]};
    if(viewTransitions) document.startViewTransition = update => {
      let captured, skipped, finish;
      const transition = {update, ready:new Promise((resolve,reject)=>{captured=resolve;skipped=reject;}), finished:new Promise(resolve=>{finish=resolve;}),
        capture(){update();captured();},finish(){finish();},skipTransition(){this.skipped=true;skipped(Error('skipped'));}};
      transitions.push(transition);return transition;
    };
    const media = {...emitter(), matches:dark};
    const reduced = {...emitter(), matches:reducedMotion};
    if(legacyMedia) { media.addListener=fn=>media.listeners.change=[fn]; delete media.addEventListener; }
    const window = {...emitter(), innerWidth:1200,innerHeight:800,KeyframeEffect:{prototype:animationSupport?{pseudoElement:null}:{}},matchMedia:query=>query.includes('reduced-motion')?reduced:query.includes('max-width')?{matches:narrow}:media};
    if(!mediaSupport) delete window.matchMedia;
    const storage = {getItem(){if(blocked)throw new Error('denied');return saved;},setItem(k,v){if(blocked)throw new Error('denied');saved=v;}};
    const timers = new Set();
    const context = vm.createContext({document,window,Element,localStorage:storage,setTimeout(fn){timers.add(fn);return fn;},clearTimeout(fn){timers.delete(fn);},CustomEvent:class{constructor(type,options){this.type=type;Object.assign(this,options);}}});
    vm.runInContext(source,context);
    if(animationLoaded)vm.runInContext(reveal,context);
    return {document,window,media,control,buttons,meta,root,scripts,transitions,animations,reduced, get saved(){return saved;},
      ready(){ready='complete';document.dispatchEvent({type:'DOMContentLoaded'});},
      loadReveal(){vm.runInContext(reveal,context);scripts.at(-1)?.onload();},
      expire(){for(const timer of timers)timer();timers.clear();},
      system(value){media.matches=value;media.dispatchEvent({type:'change'});},
      storage(value,key='newpress.colorMode',newValue=value){if(key==='newpress.colorMode'||key===null)saved=value;window.dispatchEvent({type:'storage',key,newValue});},
      restore(value){saved=value;window.dispatchEvent({type:'pageshow',persisted:true});},
      click(mode){control.dispatchEvent({type:'click',target:buttons.find(b=>b.dataset.colorMode===mode)});}
    };
  }
  test('saved dark mode applies synchronously before the controls or CSS exist',()=>{
    const f=fixture({saved:'dark'});
    assert.equal(f.document.documentElement.dataset.theme,'dark');
    assert.equal(f.meta.content,'#111721');
    f.ready(); assert.equal(f.control.hidden,false);
    assert.deepEqual(f.buttons.map(b=>b.attributes['aria-pressed']),['false','true']);
  });
  test('system preference updates live, while an explicit preference remains stable',()=>{
    const f=fixture({dark:true});f.ready();
    assert.equal(f.window.ChunnelColorMode.theme,'dark');
    assert.deepEqual(f.buttons.map(b=>b.attributes['aria-pressed']),['false','true']);
    assert.equal(f.saved,null);f.system(false);
    assert.deepEqual(f.buttons.map(b=>b.attributes['aria-pressed']),['true','false']);
    assert.equal(f.window.ChunnelColorMode.theme,'light');f.click('light');f.system(true);
    assert.equal(f.window.ChunnelColorMode.theme,'light');f.window.ChunnelColorMode.set('system');
    assert.equal(f.window.ChunnelColorMode.theme,'dark');
  });
  test('selection persists, updates the two pressed states and keeps focus on the selected button',()=>{
    const f=fixture();f.ready();f.click('dark');
    assert.equal(f.saved,'dark');assert.equal(f.buttons[1].focused,true);
    assert.deepEqual(f.buttons.map(b=>b.attributes['aria-pressed']),['false','true']);
    f.click('light');assert.equal(f.meta.content,'#f7f9fc');
  });
  test('the first direct click keeps the existing circular reveal centered on the selected button',async()=>{
    const f=fixture({viewTransitions:true});f.ready();assert.equal(f.scripts.length,0);
    f.click('dark');
    const [transition]=f.transitions;
    assert.equal(f.buttons[1].focused,true);assert.equal(f.saved,'dark');
    assert.equal(f.window.ChunnelColorMode.theme,'light');f.system(true);assert.ok(!transition.skipped);
    transition.capture();await new Promise(setImmediate);
    assert.equal(f.root.dataset.theme,'dark');
    const next=f.animations.find(a=>a.timing.pseudoElement==='::view-transition-new(root)');
    assert.deepEqual(Array.from(next.frames.clipPath),['circle(0px at 72px 40px)',`circle(${Math.ceil(Math.hypot(1128,760))}px at 72px 40px)`]);
    assert.equal(next.timing.duration,480);assert.equal(next.timing.easing,'ease-in');
    transition.finish();await new Promise(setImmediate);assert.ok(f.animations.every(a=>a.cancelled));
    assert.equal(f.scripts.length,0);
  });
  test('the new snapshot uses final control colors without completing unrelated transitions',()=>{
    const f=fixture({viewTransitions:true});f.ready();
    const finished=[];
    const css=property=>({transitionProperty:property,finish(){finished.push(this);}});
    const hover=css('color'), input=css('background-color'), pressed=css('border-top-color'), slide=css('transform');
    f.document.getAnimations=()=>f.root.dataset.theme==='dark'?[hover,input,pressed,slide]:[hover];
    f.click('dark');f.transitions[0].capture();
    assert.deepEqual(finished,[input,pressed]);
  });
  test('unsupported animation APIs leave both direct choices usable',async()=>{
    for(const options of [{viewTransitions:false},{viewTransitions:true,animationSupport:false}]){
      const f=fixture(options);f.ready();f.click('dark');
      if(f.scripts.length){f.loadReveal();await new Promise(setImmediate);}
      assert.equal(f.root.dataset.theme,'dark');assert.equal(f.transitions.length,0);
      f.click('light');await new Promise(setImmediate);assert.equal(f.root.dataset.theme,'light');
    }
  });

  test('a cold first click waits for the reveal, with a bounded fallback for slow or failed loads',async()=>{
    const first=fixture({viewTransitions:true,animationLoaded:false});first.ready();first.click('dark');
    assert.equal(first.saved,'dark');assert.equal(first.root.dataset.theme,'light');assert.equal(first.scripts.length,1);
    first.loadReveal();await new Promise(setImmediate);assert.equal(first.transitions.length,1);
    first.transitions[0].capture();assert.equal(first.root.dataset.theme,'dark');
    for(const failure of ['timeout','error']){
      const f=fixture({viewTransitions:true,animationLoaded:false});f.ready();f.click('dark');
      if(failure==='error')f.scripts[0].onerror();else f.expire();
      await new Promise(setImmediate);assert.equal(f.root.dataset.theme,'dark');
      f.loadReveal();await new Promise(setImmediate);assert.equal(f.transitions.length,0);
    }
  });

  test('a pending reveal never overwrites a later choice or a cross-tab preference',async()=>{
    for(const change of [f=>f.click('light'),f=>f.window.ChunnelColorMode.set('light'),f=>f.storage('light')]){
      const f=fixture({viewTransitions:true,animationLoaded:false});f.ready();f.click('dark');change(f);
      f.loadReveal();await new Promise(setImmediate);
      assert.equal(f.root.dataset.theme,'light');assert.equal(f.saved,'light');assert.equal(f.transitions.length,0);
    }
  });

  test('compact controls transfer focus to the opposite-mode button after each switch',()=>{
    const f=fixture({narrow:true});f.ready();f.click('dark');
    assert.equal(f.document.activeElement,f.buttons[0]);assert.equal(f.root.dataset.theme,'dark');
    f.click('light');assert.equal(f.document.activeElement,f.buttons[1]);assert.equal(f.root.dataset.theme,'light');
  });
  test('later choices, a direct set or a cross-tab write win even if a skipped update runs late',async()=>{
    for(const change of [f=>f.click('light'),f=>f.window.ChunnelColorMode.set('light'),f=>f.storage('light','newpress.colorMode','dark')]){
      const f=fixture({viewTransitions:true});f.ready();f.click('dark');
      const [stale]=f.transitions;change(f);assert.equal(stale.skipped,true);
      stale.update();stale.finish();await new Promise(setImmediate);
      assert.equal(f.root.dataset.theme,'light');assert.equal(f.saved,'light');assert.equal(f.animations.length,0);
    }
  });
  test('an OS motion change skips the animation without losing the selection',async()=>{
    const f=fixture({viewTransitions:true});f.ready();f.click('dark');
    f.reduced.matches=true;f.reduced.dispatchEvent({type:'change'});const [t]=f.transitions;
    assert.equal(t.skipped,true);t.update();t.finish();await new Promise(setImmediate);
    assert.equal(f.root.dataset.theme,'dark');assert.equal(f.animations.length,0);
  });
  test('programmatic changes, system updates and reduced-motion users apply without a view transition',()=>{
    const f=fixture({viewTransitions:true,reducedMotion:true});f.ready();f.click('dark');assert.equal(f.scripts.length,0);
    assert.equal(f.transitions.length,0);assert.equal(f.window.ChunnelColorMode.theme,'dark');
    f.window.ChunnelColorMode.set('light');
    assert.equal(f.transitions.length,0);assert.equal(f.window.ChunnelColorMode.theme,'light');
    const direct=fixture({viewTransitions:true});direct.ready();direct.window.ChunnelColorMode.set('dark');
    assert.equal(direct.transitions.length,0);assert.equal(direct.window.ChunnelColorMode.theme,'dark');
    direct.storage('light');
    assert.equal(direct.transitions.length,0);assert.equal(direct.window.ChunnelColorMode.theme,'light');
    const system=fixture({viewTransitions:true});system.ready();system.system(true);
    assert.equal(system.transitions.length,0);assert.equal(system.window.ChunnelColorMode.theme,'dark');
    system.click('dark');assert.equal(system.transitions.length,0);assert.equal(system.saved,'dark');
    const site=fixture({viewTransitions:true,siteReduced:true});site.ready();site.click('dark');
    assert.equal(site.transitions.length,0);assert.equal(site.root.dataset.theme,'dark');
  });
  test('blocked local storage still permits session changes and system tracking',()=>{
    const f=fixture({blocked:true,dark:true});f.ready();f.click('light');
    assert.equal(f.window.ChunnelColorMode.theme,'light');f.window.ChunnelColorMode.set('system');f.system(false);
    assert.equal(f.window.ChunnelColorMode.theme,'light');
  });
  test('invalid preferences and cross-tab deletion fall back to the current system',()=>{
    const f=fixture({saved:'unknown',dark:true});f.ready();assert.equal(f.window.ChunnelColorMode.mode,'system');
    f.storage('light');assert.equal(f.window.ChunnelColorMode.theme,'light');
    f.storage(null);assert.equal(f.window.ChunnelColorMode.theme,'dark');
    f.storage('dark','unrelated');assert.equal(f.window.ChunnelColorMode.mode,'system');
  });
  test('clicking the active system theme makes it an explicit preference without animating',()=>{
    for(const dark of [false,true]){
      const f=fixture({viewTransitions:true,dark});f.ready();f.click(dark?'dark':'light');
      assert.equal(f.saved,dark?'dark':'light');assert.equal(f.transitions.length,0);
      f.system(!dark);assert.equal(f.window.ChunnelColorMode.theme,dark?'dark':'light');
    }
  });

  test('focus and pointer preparation share one load, and a failed load can be retried',async()=>{
    const f=fixture({viewTransitions:true,animationLoaded:false});f.ready();
    f.control.dispatchEvent({type:'focusin'});f.control.dispatchEvent({type:'pointerdown'});
    assert.equal(f.scripts.length,1);assert.equal(f.saved,null);assert.equal(f.root.dataset.theme,'light');
    f.scripts[0].onerror();await new Promise(setImmediate);
    f.click('dark');assert.equal(f.scripts.length,2);
    f.loadReveal();await new Promise(setImmediate);f.transitions[0].capture();
    assert.equal(f.root.dataset.theme,'dark');
  });

  test('a BFCache restoration reads the current preference and invalidates a pending reveal',async()=>{
    const f=fixture({viewTransitions:true,animationLoaded:false});f.ready();f.click('dark');
    f.restore('light');f.loadReveal();await new Promise(setImmediate);
    assert.equal(f.root.dataset.theme,'light');assert.equal(f.window.ChunnelColorMode.mode,'light');
    assert.equal(f.transitions.length,0);
  });

  test('missing media queries, metadata or reveal URL retain direct theme choices',()=>{
    const basic=fixture({mediaSupport:false,metadata:false});basic.ready();basic.click('dark');
    assert.equal(basic.root.dataset.theme,'dark');assert.equal(basic.scripts.length,0);
    basic.window.ChunnelColorMode.set('system');assert.equal(basic.root.dataset.theme,'light');
    const missing=fixture({viewTransitions:true,animationLoaded:false,entryURL:''});missing.ready();missing.click('dark');
    assert.equal(missing.root.dataset.theme,'dark');assert.equal(missing.scripts.length,0);
    const legacy=fixture({legacyMedia:true});legacy.ready();legacy.system(true);
    assert.equal(legacy.root.dataset.theme,'dark');
  });

  test('non-element click targets are ignored and a nested icon retains its button action',()=>{
    const f=fixture();f.ready();
    f.control.dispatchEvent({type:'click',target:null});
    f.control.dispatchEvent({type:'click',target:{}});
    assert.equal(f.saved,null);
    const icon=Object.assign(new Element(),{closest:()=>f.buttons[1]});
    f.control.dispatchEvent({type:'click',target:icon});assert.equal(f.saved,'dark');
  });

  test('transition creation or snapshot animation failures keep the new appearance usable',async()=>{
    const throwing=fixture({viewTransitions:true});throwing.ready();
    throwing.document.startViewTransition=()=>{throw Error('capture unavailable');};throwing.click('dark');
    assert.equal(throwing.root.dataset.theme,'dark');
    const partial=fixture({viewTransitions:true});partial.ready();const animate=partial.root.animate;
    let calls=0;partial.root.animate=(...args)=>{if(++calls===2)throw Error('animation unavailable');return animate(...args);};
    partial.click('dark');partial.transitions[0].capture();await new Promise(setImmediate);
    assert.equal(partial.root.dataset.theme,'dark');assert.equal(partial.transitions[0].skipped,true);
    partial.transitions[0].finish();await new Promise(setImmediate);
    assert.ok(partial.animations.every(animation=>animation.cancelled));
    partial.window.ChunnelColorMode.set('light');assert.equal(partial.root.dataset.theme,'light');
  });

  test('only color CSS transitions finish while non-transition animations keep running',()=>{
    const f=fixture({viewTransitions:true});f.ready();
    const ordinary={finish(){throw Error('unrelated animation must not finish');}};
    const invalid={transitionProperty:5,finish(){throw Error('not a CSS transition');}};
    f.document.getAnimations=()=>f.root.dataset.theme==='dark'?[ordinary,invalid]:[];
    f.click('dark');f.transitions[0].capture();assert.equal(f.root.dataset.theme,'dark');
  });

  test('public and embedded controls expose only two accessible direct choices',()=>{
    const theme=fs.readFileSync('themes/chunnel/templates/partials/color-mode.html','utf8');
    assert.equal(theme,fs.readFileSync('internal/coreui/templates/partials/color-mode.html','utf8'));
    assert.deepEqual([...theme.matchAll(/data-color-mode="([^"]+)"/g)].map(match=>match[1]),['light','dark']);
    assert.match(theme,/role="group"/);assert.match(theme,/aria-label="浅色模式"/);assert.match(theme,/aria-label="深色模式"/);
    assert.doesNotMatch(theme,/<details|<summary|color-mode-options/);
    for(const name of ['chunnel-color-mode','chunnel-color-reveal']){
      assert.equal(fs.readFileSync(`themes/chunnel/assets/js/${name}.js`,'utf8'),fs.readFileSync(`internal/coreui/assets/js/${name}.js`,'utf8'));
    }
  });
  test('color-mode events carry the resolved theme for isolated preview subscribers',()=>{
    const f=fixture();f.ready();let event;
    f.document.addEventListener('chunnel:color-mode',value=>event=value);f.click('dark');
    assert.equal(event.detail.theme,'dark');assert.equal(event.detail.mode,'dark');
    const preview={documentElement:{dataset:{}}};
    syncPreviewColorMode({documentElement:f.root},preview);
    assert.equal(preview.documentElement.dataset.theme,'dark');
    assert.equal(preview.documentElement.dataset.colorMode,'dark');
    syncPreviewColorMode({documentElement:{dataset:{}}},preview);
    assert.equal(preview.documentElement.dataset.theme,'light');
    assert.equal(preview.documentElement.dataset.colorMode,'system');
  });
  test('component color references remain syntactically valid after semantic migration',()=>{
    const dir='themes/chunnel/assets/css';
    for(const file of fs.readdirSync(dir).filter(file=>file.endsWith('.css'))){
      postcss.parse(fs.readFileSync(path.join(dir,file),'utf8')).walkDecls(d=>{
        assert.doesNotMatch(d.value,/var\(--var|var\(\s*[^-\s]/,file+': '+d.value);
      });
    }
  });
}
