-- Extend retained shared evidence without rewriting prior migrations or rows.
ALTER TABLE np_plugin_shared_quarantines
    DROP CONSTRAINT np_plugin_shared_quarantines_record_type_check;
ALTER TABLE np_plugin_shared_quarantines
    ADD CONSTRAINT np_plugin_shared_quarantines_record_type_check
    CHECK (record_type IN ('authority', 'content_effect', 'webhook_intent'));
