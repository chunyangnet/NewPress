// These enhancements all start with the article/page editor. Keep their
// dependency order, sharing imported helpers and one compression dictionary.
import './resource-loader';
import './editor-media-loader';
import './article-badge-editor';
import '../shared/scroll';
import './editor-workspace';
// The editor always navigates as a complete document. The list-region
// navigation controller has no supported editor route and returns unused.
import '../shared/site-time-global';
import { initializeSwitches } from './admin-switch';
import './admin-feature-status';
import '../shared/selects';
import './editor-recovery';
import './admin-feedback';
import './admin-editor';
import './seo-editor';
// Source formatting is immediate; the emoji picker starts on explicit use.
import './editor-emoji-loader';
// Install tool launchers before the continuous editor mounts. They capture its
// current state only on activation, and optional tools still load on demand.
import './component-loader';
import './editor-tools-loader';

// The writing workspace owns saving and navigation. Its hidden ordinary
// sidebar/topbar and generic savebars do not need their controllers.
initializeSwitches();
