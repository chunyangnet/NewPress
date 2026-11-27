-- Reviewed reissues retain their command identity on both sides of archiving.
-- Nullable columns preserve older task records; admission serializes moves
-- and detects duplicate request identities across the two tables.
ALTER TABLE np_plugin_jobs ADD COLUMN reissue_id text CHECK (reissue_id IS NULL OR reissue_id ~ '^[a-f0-9]{64}$');
CREATE UNIQUE INDEX np_plugin_jobs_reissue ON np_plugin_jobs(reissue_id) WHERE reissue_id IS NOT NULL;
ALTER TABLE np_plugin_job_archives ADD COLUMN reissue_id text CHECK (reissue_id IS NULL OR reissue_id ~ '^[a-f0-9]{64}$');
CREATE UNIQUE INDEX np_plugin_job_archives_reissue ON np_plugin_job_archives(reissue_id) WHERE reissue_id IS NOT NULL;
