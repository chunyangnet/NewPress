export const configFields = ['app_id', 'author', 'thumb_media_id'] as const;
export const configChecks = ['enabled', 'collect_enabled', 'collect_drafts', 'auto_sync', 'auto_publish', 'include_source', 'open_comment', 'fans_comment'] as const;
export type ConfigField = typeof configFields[number];
export type ConfigCheck = typeof configChecks[number];
export type WeChatConfig = Record<ConfigField, string> & Record<ConfigCheck, boolean> & { version: number; has_secret: boolean; since: string };
export type ConfigInput = Record<ConfigField, string> & Record<ConfigCheck, boolean> & { version: number; app_secret: string };
export type ConfigValues = Record<ConfigField | 'app_secret', string> & Record<ConfigCheck | 'clear_secret', boolean>;
export const jobStatuses = ['queued','working','draft','publishing','published','failed','uncertain','skipped'] as const;
export type JobStatus = typeof jobStatuses[number];
export interface WeChatJob { id: string; app_id: string; post_id: number; version: number; title: string; actor: string; auto_publish: boolean; status: JobStatus; stage: 'prepare'|'draft'|'publish'|'poll'; media_id?: string; publish_id?: string; article_id?: string; article_url?: string; error?: string; attempts: number; created_at: string; updated_at: string; next_at: string; lease_until: string }
export interface WeChatPost { id: number; version: number; title: string; status: 'draft'|'publish'|'future'; cover: string }
export interface Workspace { user_id: number; config: WeChatConfig; can_sync: boolean; can_collect: boolean; posts: WeChatPost[]; selected_post: WeChatPost|null; jobs: WeChatJob[]; job_page: number; job_pages: number; total_jobs: number; filtered_jobs: number; active_jobs: number; attention_jobs: number }
export interface CollectedArticle { key: string; url: string; title: string; author: string; excerpt: string; body: string; cover: string; preview: string; images: string[]; warnings: string[] }
export interface CollectFailure { url: string; message: string }
export interface CollectRow { key: string; title?: string; action: 'create'|'created'|'skip'|'changed'|'conflict'; post_id?: number; message?: string }
export interface CollectPlan { items: CollectRow[]; new: number; existing: number; conflicts: number }
export interface CollectPreview { articles: CollectedArticle[]; failures: CollectFailure[]; receipts: Record<string,string>; expires: string; plan: CollectPlan }
export interface CollectImport { applied: true; plan: CollectPlan & {items: (CollectRow & {post_id:number; title:string})[]} }
export interface SyncInput { post_id: number; version: number; config_version: number }
export interface SyncCheck extends SyncInput { title: string; auto_publish: boolean; ready: boolean; title_chars: number; content_chars: number; content_bytes: number; images: number; checked_images: number; upload_bytes: number; issues: {field:string;message:string}[]; warnings: string[]; checked_at: string; stale?: boolean }
export interface DraftChoice { key: string; digest: string; selected: boolean }
export interface CollectDraft { version: 1; owner: number; at: number; input: string; baseline: string; choices: DraftChoice[]; uncertain: boolean }
export interface DraftStore { read(): CollectDraft|null; save(input:string,baseline:string,choices:DraftChoice[],uncertain:boolean):CollectDraft|null; clear():void }
export interface WeChatDrafts { create(owner:number):DraftStore; fingerprints(articles:CollectedArticle[]):Promise<Map<string,string>> }
declare global { interface Window { ChunnelWeChatDrafts?: WeChatDrafts } }
