ALTER TABLE np_navigation_locations
    DROP CONSTRAINT np_navigation_locations_valid;

ALTER TABLE np_navigation_locations
    ADD CONSTRAINT np_navigation_locations_valid
    CHECK (location IN ('primary', 'utility', 'mobile', 'mobile_action', 'footer'));
