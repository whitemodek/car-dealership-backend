CREATE TABLE staff (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL UNIQUE CHECK (email = lower(email)),
    password_hash text NOT NULL,
    role text NOT NULL CHECK (role IN ('admin', 'manager')),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    token_hash text PRIMARY KEY,
    staff_id uuid NOT NULL REFERENCES staff(id),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expiry_idx ON sessions(expires_at);
CREATE INDEX sessions_staff_idx ON sessions(staff_id);

CREATE TABLE models (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    make text NOT NULL CHECK (length(make) BETWEEN 1 AND 80),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    UNIQUE(make, name)
);
CREATE TABLE trims (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id uuid NOT NULL REFERENCES models(id),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    UNIQUE(model_id, name),
    UNIQUE(id, model_id)
);
CREATE UNIQUE INDEX models_case_insensitive_idx ON models(lower(make),lower(name));
CREATE UNIQUE INDEX trims_case_insensitive_idx ON trims(model_id,lower(name));
CREATE TABLE cars (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id uuid NOT NULL REFERENCES models(id),
    trim_id uuid,
    vin text NOT NULL UNIQUE CHECK (vin ~ '^[A-HJ-NPR-Z0-9]{17}$'),
    condition text NOT NULL CHECK (condition IN ('new', 'used')),
    year integer NOT NULL CHECK (year BETWEEN 1886 AND 2100),
    mileage_km integer NOT NULL CHECK (mileage_km >= 0),
    price_minor bigint NOT NULL CHECK (price_minor > 0 AND price_minor <= 9007199254740991),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    color text NOT NULL CHECK (length(color) BETWEEN 1 AND 80),
    fuel text NOT NULL CHECK (fuel IN ('petrol', 'diesel', 'hybrid', 'electric')),
    transmission text NOT NULL CHECK (transmission IN ('manual', 'automatic')),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 10000),
    publication text NOT NULL DEFAULT 'draft' CHECK (publication IN ('draft', 'published', 'archived')),
    sale_status text NOT NULL DEFAULT 'available' CHECK (sale_status IN ('available', 'sold', 'withdrawn')),
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY(trim_id, model_id) REFERENCES trims(id, model_id)
);
CREATE INDEX cars_catalog_idx ON cars(publication, sale_status, created_at DESC, id);
CREATE INDEX cars_model_idx ON cars(model_id);
CREATE INDEX cars_price_idx ON cars(currency, price_minor, id) WHERE publication = 'published';
CREATE TABLE car_photos (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    car_id uuid NOT NULL REFERENCES cars(id),
    url text NOT NULL CHECK (length(url) <= 2048),
    position integer NOT NULL CHECK (position BETWEEN 0 AND 19),
    UNIQUE(car_id, position)
);
CREATE TABLE price_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    car_id uuid NOT NULL REFERENCES cars(id),
    old_price_minor bigint NOT NULL,
    new_price_minor bigint NOT NULL,
    currency text NOT NULL,
    actor_id uuid NOT NULL REFERENCES staff(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX price_history_car_idx ON price_history(car_id, id DESC);

CREATE TABLE leads (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    car_id uuid REFERENCES cars(id),
    kind text NOT NULL CHECK (kind IN ('purchase', 'callback', 'test_drive')),
    customer_name text NOT NULL CHECK (length(customer_name) BETWEEN 1 AND 100),
    phone text NOT NULL CHECK (length(phone) BETWEEN 5 AND 30),
    email text NOT NULL DEFAULT '' CHECK (length(email) <= 254),
    message text NOT NULL DEFAULT '' CHECK (length(message) <= 2000),
    consent_at timestamptz NOT NULL DEFAULT now(),
    status text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'contacted', 'qualified', 'closed')),
    assigned_to uuid REFERENCES staff(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX leads_queue_idx ON leads(status, created_at DESC, id);
CREATE TABLE reservations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    car_id uuid NOT NULL REFERENCES cars(id),
    lead_id uuid NOT NULL REFERENCES leads(id),
    idempotency_key uuid NOT NULL UNIQUE,
    request_hash text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'confirmed', 'cancelled', 'expired', 'completed')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX reservations_one_active_idx ON reservations(car_id) WHERE status IN ('active', 'confirmed');
CREATE INDEX reservations_expiry_idx ON reservations(expires_at) WHERE status IN ('active', 'confirmed');

CREATE TABLE audit_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id uuid REFERENCES staff(id),
    action text NOT NULL,
    entity_id uuid NOT NULL,
    details jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_entity_idx ON audit_log(entity_id, id DESC);
CREATE TABLE outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL,
    entity_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    attempts integer NOT NULL DEFAULT 0,
    delivered_at timestamptz,
    failed_at timestamptz
);
CREATE INDEX outbox_pending_idx ON outbox(available_at) WHERE delivered_at IS NULL AND failed_at IS NULL;
CREATE TABLE rate_limits (
    key_hash text NOT NULL,
    bucket timestamptz NOT NULL,
    hits integer NOT NULL,
    PRIMARY KEY(key_hash, bucket)
);
