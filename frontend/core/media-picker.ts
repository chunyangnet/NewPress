import { initializeMediaPicker } from './media-picker-runtime';
import { installMediaEntrypoints } from './media-entrypoints';

initializeMediaPicker();
installMediaEntrypoints(request => window.NewPressMediaRuntime?.open(request) ?? Promise.resolve());
