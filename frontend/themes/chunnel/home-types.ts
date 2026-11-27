export type HomeLoad = (name: string, ready?: () => unknown) => Promise<void>;
export interface HomeAPI { refreshFeed(): void; stop(): void }
export interface HomeMemberAPI { paint(data: unknown, owner: number): boolean }

declare global {
  interface Window {
    ChunnelHome?: HomeAPI;
    ChunnelHomeMember?: HomeMemberAPI;
    ChunnelHomeFeaturesReady?: boolean;
    ChunnelHomeFeedReady?: boolean;
  }
}
