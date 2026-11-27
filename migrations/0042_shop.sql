-- Shop catalog and order reservations are committed under one aggregate lock.
CREATE TABLE np_shop_state (
 id smallint PRIMARY KEY CHECK(id=1),
 data jsonb NOT NULL CHECK(jsonb_typeof(data)='object')
);
