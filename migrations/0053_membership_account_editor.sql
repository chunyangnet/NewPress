-- Administrative account editing records a dated membership change in the
-- same append-only ledger used by grants, renewals and purchases.
ALTER TABLE np_progress_entries DROP CONSTRAINT np_progress_entries_kind_check;
ALTER TABLE np_progress_entries ADD CONSTRAINT np_progress_entries_kind_check
 CHECK (kind IN ('experience_adjust','reward_check_in','reward_registration',
 'reward_publication','membership_grant','membership_renew','membership_revoke',
 'membership_purchase','membership_set'));
