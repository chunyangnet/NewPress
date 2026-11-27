export interface ConsentChoice { necessary: true; analytics: boolean; version: 1; savedAt: string }
export interface CookieConsentAPI { allowsAnalytics(): boolean }
