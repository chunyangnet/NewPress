export interface MediaImage {
  id: number;
  status: 'active';
  kind: 'image';
  public_url: string;
  filename: string;
  title: string;
  alt_text: string;
  caption: string;
  size_bytes: number;
  width: number;
  height: number;
}

export interface MediaList {
  items: MediaImage[];
  pagination: { page: number; pages: number; total: number };
  can_upload: boolean;
  max_upload_bytes: number;
}

export interface EditorSourceSelection {
  start: number;
  end: number;
  value: string;
}

export interface MediaPickerOptions {
  title?: string;
  uploadSource?: string;
  file?: File | null;
  disabled?(): boolean;
  onSelect(url: string, item: MediaImage): void | Promise<void>;
}

export interface MediaPicker {
  open(launcher: HTMLElement, options: MediaPickerOptions): Promise<void> | undefined;
}

export type ComponentMediaSelection = (url: string, item: MediaImage) => void;
