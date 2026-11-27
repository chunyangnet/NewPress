export interface ChoiceOpenOptions {
  last?: boolean;
  keyboard?: boolean;
}

/** Optional rich picker loaded by the small native-select entry. */
export interface ChoiceMenuAPI {
  prepare(select: HTMLSelectElement): void;
  open(select: HTMLSelectElement, options?: ChoiceOpenOptions): void;
  reset(select: HTMLSelectElement): void;
  prune(): void;
}
