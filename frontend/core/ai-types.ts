import type * as WritingCore from '../shared/ai-writing-helpers';
type AICore = Pick<typeof WritingCore, 'splitDocument'|'renderMarkdown'|'SSEDecoder'|'readStream'|'ThinkingTrace'|'GenerationStats'|'byteLength'|'MAX_OUTPUT'|'MAX_THINKING'|'chooseConnection'|'briefProblem'>;
declare global {
  interface Window {
    ChunnelAICore?: AICore;
    ChunnelAIEditorReady?: boolean;
    ChunnelAIImagesReady?: boolean;
  }
}
