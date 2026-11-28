import { localBadgeImage } from '../shared/recognition-data';
import type { PublicBadge, PublicComment } from '../shared/experience-types';

const iconNames: readonly string[] = ['award','sparkles','star','heart','diamond','shield-check','badge-check','zap','pen-line','newspaper','x'];
export const node = <K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] => { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; if (cls) el.className = cls; return el; };
const icon = (name?: string) => { const svg = document.createElementNS('http://www.w3.org/2000/svg','svg'); svg.setAttribute('class','icon'); svg.setAttribute('fill','none'); svg.setAttribute('stroke','currentColor'); svg.setAttribute('stroke-width','1.75'); svg.setAttribute('stroke-linecap','round'); svg.setAttribute('stroke-linejoin','round'); svg.setAttribute('viewBox','0 0 24 24'); svg.setAttribute('aria-hidden','true'); const use = document.createElementNS('http://www.w3.org/2000/svg','use'); use.setAttribute('href','#icon-' + (iconNames.includes(name || '') ? name : 'award')); svg.append(use); return svg; };
export function colors(el: HTMLElement) {
  const color = el.dataset.identityColor, bg = el.dataset.identityBackground;
  if (color && /^#[0-9a-f]{6}$/.test(color)) {
    el.style.setProperty('--identity-accent',color);
    const rgb = [1,3,5].map(i => parseInt(color.slice(i,i+2),16)/255).map(v => v <= .04045 ? v/12.92 : ((v+.055)/1.055)**2.4);
    el.style.setProperty('--identity-on',(rgb[0] || 0)*.2126+(rgb[1] || 0)*.7152+(rgb[2] || 0)*.0722 > .179 ? '#17253d' : '#ffffff');
  }
  if (bg && /^#[0-9a-f]{6}$/.test(bg)) el.style.setProperty('--identity-bg',bg);
}
const localImage = localBadgeImage;
export function badge(b: Partial<PublicBadge>, options: { interactive?: boolean; decorative?: boolean } = {}) {
  const medal = b.kind === 'medal', interactive = medal && options.interactive !== false;
  const level = b.kind === 'level' && Number.isInteger(b.level) && (b.level || 0) > 0 && (b.level || 0) <= 50;
  const el = node(interactive ? 'button' : 'span', undefined, 'identity-badge' + (medal ? ' identity-medal' : level ? ' identity-level' : ''));
  el.dataset.identityKind=b.kind || ''; el.dataset.identityStyle=b.style || ''; el.dataset.identityColor=b.color || ''; el.dataset.identityBackground=b.background || '';
  if (medal) {
    el.dataset.identityName=b.name || ''; el.dataset.identityDescription=b.description || '';
    el.dataset.identityGranted=b.granted_at || ''; el.dataset.identityExpires=b.expires_at || '';
    el.dataset.identityImage=localImage(b.image) ? b.image : ''; el.dataset.identityIcon=iconNames.includes(b.icon || '') ? b.icon || 'award' : 'award';
    if (interactive) { (el as HTMLButtonElement).type='button'; el.dataset.medalDetails=''; el.setAttribute('aria-label',b.name+' · 查看勋章详情'); el.setAttribute('aria-haspopup','dialog'); el.setAttribute('aria-expanded','false'); }
    else if (options.decorative) el.setAttribute('aria-hidden','true');
    else { el.setAttribute('role','img'); el.setAttribute('aria-label',b.name || ''); el.title=b.name || ''; }
  } else el.title = [level ? 'LV' + b.level : '', b.name,b.description].filter(Boolean).join(' · ');
  if (level) {
    el.setAttribute('aria-label',`等级 ${b.level} · ${b.name}`);
    const art=node('span',undefined,'identity-level-art'); art.setAttribute('aria-hidden','true'); art.append(node('span','LV','identity-level-prefix'),node('span',String(b.level),'identity-level-number')); el.append(art);
  } else {
    const art=medal ? node('span',undefined,'identity-medal-art') : el;
    if (localImage(b.image)) { const img=node('img'); img.src=b.image; img.alt=''; img.width=img.height=medal ? 28 : 20; art.append(img); } else art.append(icon(b.icon));
    if (medal) { art.setAttribute('aria-hidden','true'); el.append(art); } else el.append(node('span',b.name));
  }
  colors(el); return el;
}
let activeMedal: HTMLButtonElement | null = null, detailPanel: HTMLElement | null = null, detailSequence = 0;
export const medalDate = (raw?: string) => {
  if (!raw || raw.startsWith('0001-')) return '';
  const value = new Date(raw); if (Number.isNaN(value.getTime())) return '';
  return window.ChunnelTime?.formatDateTime(raw) || value.toLocaleString('zh-CN', { year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit' });
};
function closeMedal(restoreFocus = false) {
  if (!activeMedal) return;
  const trigger=activeMedal; activeMedal=null;
  trigger.setAttribute('aria-expanded','false'); trigger.removeAttribute('aria-controls');
  detailPanel?.remove(); detailPanel=null;
  if (restoreFocus && trigger.isConnected) trigger.focus({preventScroll:true});
}
function positionMedal() {
  if (!activeMedal || !detailPanel) return;
  if (!activeMedal.isConnected || !activeMedal.getClientRects().length) { closeMedal(); return; }
  const viewport=window.visualViewport, leftEdge=viewport?.offsetLeft || 0, topEdge=viewport?.offsetTop || 0;
  const width=viewport?.width || document.documentElement.clientWidth, height=viewport?.height || window.innerHeight;
  const rect=activeMedal.getBoundingClientRect(), gap=10, margin=12;
  if (rect.bottom < topEdge || rect.top > topEdge+height || rect.right < leftEdge || rect.left > leftEdge+width) { closeMedal(); return; }
  detailPanel.style.maxWidth=Math.max(0,width-margin*2)+'px';
  detailPanel.style.maxHeight=Math.max(0,height-margin*2)+'px';
  const size=detailPanel.getBoundingClientRect();
  const below=topEdge+height-rect.bottom-gap-margin, above=rect.top-topEdge-gap-margin;
  const side=below >= size.height || below >= above ? 'bottom' : 'top';
  const left=Math.max(leftEdge+margin,Math.min(rect.left+(rect.width-size.width)/2,leftEdge+width-margin-size.width));
  const top=Math.max(topEdge+margin,Math.min(side==='bottom' ? rect.bottom+gap : rect.top-gap-size.height,topEdge+height-margin-size.height));
  detailPanel.dataset.side=side; detailPanel.style.left=left+'px'; detailPanel.style.top=top+'px';
  detailPanel.style.setProperty('--medal-arrow',Math.max(22,Math.min(size.width-22,rect.left+rect.width/2-left))+'px');
}
function openMedal(trigger: HTMLButtonElement) {
  if (activeMedal===trigger) { closeMedal(true); return; }
  closeMedal(); activeMedal=trigger;
  const d=trigger.dataset, id='medal-detail-'+(++detailSequence);
  const panel=node('section',undefined,'identity-popover'); detailPanel=panel; panel.id=id; panel.tabIndex=-1;
  panel.setAttribute('role','dialog'); panel.setAttribute('aria-label',d.identityName+' · 勋章详情'); panel.setAttribute('aria-describedby',id+'-description');
  panel.dataset.identityColor=d.identityColor || ''; panel.dataset.identityBackground=d.identityBackground || ''; colors(panel);
  const surface=node('div',undefined,'identity-popover-surface'), header=node('div',undefined,'identity-popover-heading');
  const emblem=badge({kind:'medal',name:d.identityName,icon:d.identityIcon,image:d.identityImage,color:d.identityColor,background:d.identityBackground,style:d.identityStyle},{interactive:false});
  const heading=node('div',undefined,'identity-popover-title'); heading.append(node('span','社区勋章','recognition-eyebrow'),node('h3',d.identityName));
  const close=node('button',undefined,'identity-popover-close'); close.type='button'; close.setAttribute('aria-label','关闭勋章详情'); close.append(icon('x')); close.addEventListener('click',()=>closeMedal(true));
  header.append(emblem,heading,close);
  const description=node('p',d.identityDescription || '每一份投入，都值得被看见。','identity-popover-description'); description.id=id+'-description';
  const dates=node('dl',undefined,'identity-popover-dates'), granted=medalDate(d.identityGranted), expires=medalDate(d.identityExpires);
  if (granted) { const row=node('div'); row.append(node('dt','获得时间'),node('dd',granted)); dates.append(row); }
  const expiry=node('div'); expiry.append(node('dt','有效期'),node('dd',expires ? '至 '+expires : '长期有效')); dates.append(expiry);
  surface.append(header,description,dates); panel.append(surface); document.body.append(panel);
  if (typeof panel.showPopover === 'function') { panel.setAttribute('popover','manual'); panel.showPopover(); }
  trigger.setAttribute('aria-expanded','true'); trigger.setAttribute('aria-controls',id);
  positionMedal(); panel.focus({preventScroll:true});
}
// Delegation covers both server-rendered identities and newly posted comments.
document.addEventListener('click',event=>{const trigger=event.target instanceof Element ? event.target.closest<HTMLButtonElement>('[data-medal-details]') : null; if(trigger && !trigger.disabled){event.preventDefault();openMedal(trigger);}else if(activeMedal && !detailPanel?.contains(event.target instanceof Node ? event.target : null))closeMedal();});
document.addEventListener('keydown',event=>{
  if (!activeMedal) return;
  if (event.key==='Escape') { event.preventDefault(); event.stopPropagation(); closeMedal(true); }
  else if (event.key==='Tab' && detailPanel?.contains(document.activeElement) && (event.shiftKey || document.activeElement!==detailPanel)) {
    // Continue from the original badge instead of tabbing out of the document
    // from the floating panel appended at the end of the body.
    closeMedal(true); if(event.shiftKey) event.preventDefault();
  }
});
document.addEventListener('focusin',event=>{if(activeMedal && event.target!==activeMedal && !detailPanel?.contains(event.target instanceof Node ? event.target : null))closeMedal();});
document.addEventListener('scroll',event=>{if(activeMedal && !detailPanel?.contains(event.target instanceof Node ? event.target : null))positionMedal();},true);
window.addEventListener('resize',positionMedal);
window.addEventListener('pagehide',()=>closeMedal());
window.visualViewport?.addEventListener('resize',positionMedal);
window.visualViewport?.addEventListener('scroll',positionMedal);
export function decorateComment(meta: HTMLElement, item: PublicComment) {
  if (Array.isArray(item.badges) && item.badges.length) { const group=node('span',undefined,'identity-badges'); item.badges.forEach(b=>group.append(badge(b))); meta.append(group); }
  if (item.support?.supported) { const el=node('span',undefined,'comment-support-badge'); el.append(icon('heart'),node('span',item.support.label || '已打赏')); meta.append(el); }
}
window.NewPressRecognition = Object.freeze({badge,decorateComment,colors});
document.querySelectorAll<HTMLElement>('[data-identity-color]').forEach(colors);
