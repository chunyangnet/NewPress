export interface SurfaceValues { x: number; y: number; scale: number; opacity: number }
export interface SurfaceOptions { duration?: number; lively?: boolean }
export interface SurfaceAnimation { finished: Promise<void>; cancel(): void }
export interface MotionAPI {
  surface(element: HTMLElement, from: Partial<SurfaceValues>, to: Partial<SurfaceValues>, options?: SurfaceOptions): SurfaceAnimation;
}
