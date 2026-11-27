export interface ScrollPosition { top?: number; left?: number }

export interface ScrollAPI {
  keep<T>(change: () => T, position?: ScrollPosition): T;
  restore(top: number, left?: number): void;
  remember(): void;
  error(region: Element, message: string): void;
}
