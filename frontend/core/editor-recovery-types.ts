export interface PendingSnapshot {
  key: string;
  route: string;
  body: string;
  wasDirty: boolean;
  createdAt: number;
}
export interface RecoveryPending extends PendingSnapshot {
  id: number;
  version: number;
  action: string;
  signature: string;
  uncertain: true;
}
export interface RecoverySnapshot {
  route: string;
  draft: [string, string][];
  pending: PendingSnapshot | null;
}
export interface RecoveryRecord extends RecoverySnapshot {
  schema: 1;
  owner: string;
  id: string;
  updated: number;
  expires: number;
  pending: RecoveryPending | null;
  raw: string;
}
export interface RecoveryOptions {
  owner: string;
  hours: number;
  allowed: boolean;
  route(): string;
  pending(): PendingSnapshot | null;
  idle(): boolean;
  dirty(): boolean;
  signature(): string;
  check(): Promise<boolean>;
  restore(record: RecoveryRecord): void;
  settled(): void;
  changed(): void;
  persistable(): boolean;
  snapshot(): RecoverySnapshot;
}
