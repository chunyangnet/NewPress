-- Retain idempotency receipts after deletion to prevent retries recreating posts.
ALTER TABLE np_content_submissions DROP CONSTRAINT np_content_submissions_post_id_fkey;
