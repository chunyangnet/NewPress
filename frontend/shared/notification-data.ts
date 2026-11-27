import { object, text, integer, flag } from './account-data';
export const preferenceFields = ['reply', 'mention', 'publication', 'moderation', 'email', 'weekly'] as const;
export type PreferenceField = typeof preferenceFields[number];
export type Preferences = Record<PreferenceField, boolean> & { version: number };
export interface NotificationPreferences { preferences: Preferences; email_available: boolean; timezone: string; mail_status: { pending: number; failed: number; uncertain: number } }
export function parsePreferences(value: unknown): NotificationPreferences {
  const row = object(value), prefs = object(row.preferences), counts = object(row.mail_status);
  return { preferences: { version: integer(prefs.version), reply: flag(prefs.reply), mention: flag(prefs.mention), publication: flag(prefs.publication), moderation: flag(prefs.moderation), email: flag(prefs.email), weekly: flag(prefs.weekly) },
    email_available: flag(row.email_available), timezone: text(row.timezone), mail_status: { pending: integer(counts.pending), failed: integer(counts.failed), uncertain: integer(counts.uncertain) } };
}
