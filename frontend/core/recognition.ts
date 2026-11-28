import './recognition-view';
import { mountTips } from './recognition-tips';
import { mountIdentity } from './recognition-identity';

document.querySelectorAll<HTMLElement>('[data-article-tips]').forEach(mountTips);
document.querySelectorAll<HTMLElement>('[data-identity-panel]').forEach(mountIdentity);
