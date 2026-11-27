-- Payment configuration contains only encrypted merchant credentials. The
-- aggregate lock serializes reservations and provider-transaction claims.
CREATE TABLE np_commerce_state (
 id smallint PRIMARY KEY CHECK(id=1),
 data jsonb NOT NULL CHECK(jsonb_typeof(data)='object')
);
