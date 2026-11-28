import type { WritingConnections, WritingPreference } from './ai-writing-data';
export { mediaURL } from './ai-writing-data';
  export const IMAGE_PROTOCOLS = ['openai_images', 'gemini_images', 'gemini_interactions', 'seedream'];
  export function imageProfile(protocol: string | undefined, model = '') {
    const openai = protocol === 'openai_images', seedream = protocol === 'seedream';
    const legacyGemini = protocol === 'gemini_images' && /gemini-2\.5-flash-image/.test(model);
    return {
      ratios: openai ? ['1:1', '3:2', '2:3'] : ['1:1', '3:2', '2:3', '16:9', '9:16'],
      resolutions: openai || legacyGemini ? [''] : seedream ? ['', '2K', '4K'] : ['', '1K', '2K', '4K'],
      quality: openai,
    };
  }
  export function chooseImageConnection(data: WritingConnections, preference: WritingPreference = {}) {
    const providers = (Array.isArray(data?.providers) ? data.providers : []).filter(item => IMAGE_PROTOCOLS.includes(item.protocol));
    const provider = providers.find(item => item.id === preference?.provider_id) || providers.find(item => item.id === data?.default_image_provider) || providers[0];
    if (!provider) return {provider_id: '', model: ''};
    const chosen = preference?.models?.[provider.id];
    return {provider_id: provider.id, model: chosen !== undefined && provider.models.includes(chosen) ? chosen : provider.default_model || provider.models[0] || ''};
  }
