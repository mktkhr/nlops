-- nlops モックサービスのスキーマ。
--
-- 各サービスは自分の schema しか参照しない（データ所有権の分離）。
-- 権限フィルタに必要な region は各サービス側へ非正規化して持たせる。
-- サービス間で JOIN させないための意図的な設計。

DROP SCHEMA IF EXISTS customer, orders, inventory, shipping, billing CASCADE;

CREATE SCHEMA customer;
CREATE SCHEMA orders;
CREATE SCHEMA inventory;
CREATE SCHEMA shipping;
CREATE SCHEMA billing;

-- ---------- customer ----------
CREATE TABLE customer.customers (
    customer_id  text PRIMARY KEY,
    name         text   NOT NULL,
    email        text   NOT NULL,
    phone        text   NOT NULL,
    region       text   NOT NULL CHECK (region IN ('EAST','WEST')),
    status       text   NOT NULL CHECK (status IN ('ACTIVE','INACTIVE','SUSPENDED')),
    sales_rep    text   NOT NULL,
    credit_rank  text   NOT NULL,
    credit_limit bigint NOT NULL,
    reviewed_at  date   NOT NULL
);

CREATE TABLE customer.contacts (
    contact_id  text PRIMARY KEY,
    customer_id text NOT NULL REFERENCES customer.customers(customer_id),
    name        text NOT NULL,
    role        text NOT NULL,
    email       text NOT NULL
);

-- ---------- orders ----------
CREATE TABLE orders.orders (
    order_id     text PRIMARY KEY,
    customer_id  text   NOT NULL,
    region       text   NOT NULL,  -- 権限フィルタ用に非正規化
    status       text   NOT NULL CHECK (status IN ('PLACED','CONFIRMED','SHIPPED','DELIVERED','CANCELLED')),
    ordered_at   date   NOT NULL,
    total_amount bigint NOT NULL
);

CREATE TABLE orders.order_items (
    order_id     text   NOT NULL REFERENCES orders.orders(order_id),
    line_no      int    NOT NULL,
    product_id   text   NOT NULL,
    product_name text   NOT NULL,
    quantity     int    NOT NULL,
    unit_price   bigint NOT NULL,
    PRIMARY KEY (order_id, line_no)
);

-- ---------- inventory ----------
CREATE TABLE inventory.products (
    product_id   text PRIMARY KEY,
    name         text    NOT NULL,
    category     text    NOT NULL,
    unit_price   bigint  NOT NULL,
    discontinued boolean NOT NULL DEFAULT false
);

CREATE TABLE inventory.warehouses (
    warehouse_id text PRIMARY KEY,
    name         text NOT NULL,
    region       text NOT NULL
);

CREATE TABLE inventory.stock (
    product_id   text NOT NULL REFERENCES inventory.products(product_id),
    warehouse_id text NOT NULL REFERENCES inventory.warehouses(warehouse_id),
    quantity     int  NOT NULL,
    reserved     int  NOT NULL,
    PRIMARY KEY (product_id, warehouse_id)
);

-- ---------- shipping ----------
CREATE TABLE shipping.carriers (
    carrier text PRIMARY KEY,
    name    text NOT NULL
);

CREATE TABLE shipping.shipments (
    shipment_id  text PRIMARY KEY,
    order_id     text NOT NULL,
    region       text NOT NULL,  -- 権限フィルタ用に非正規化
    status       text NOT NULL CHECK (status IN ('PREPARING','IN_TRANSIT','DELIVERED','FAILED')),
    carrier      text NOT NULL REFERENCES shipping.carriers(carrier),
    shipped_at   date,
    estimated_at date,
    confidence   text NOT NULL CHECK (confidence IN ('HIGH','MEDIUM','LOW'))
);

CREATE TABLE shipping.tracking_events (
    shipment_id text        NOT NULL REFERENCES shipping.shipments(shipment_id),
    seq         int         NOT NULL,
    occurred_at timestamptz NOT NULL,
    location    text        NOT NULL,
    event       text        NOT NULL,
    PRIMARY KEY (shipment_id, seq)
);

-- ---------- billing ----------
CREATE TABLE billing.invoices (
    invoice_id  text PRIMARY KEY,
    customer_id text   NOT NULL,
    order_id    text   NOT NULL,
    region      text   NOT NULL,  -- 権限フィルタ用に非正規化
    status      text   NOT NULL CHECK (status IN ('ISSUED','PAID','OVERDUE','VOID')),
    issued_at   date   NOT NULL,
    due_at      date   NOT NULL,
    amount      bigint NOT NULL
);

CREATE TABLE billing.payments (
    invoice_id  text PRIMARY KEY REFERENCES billing.invoices(invoice_id),
    paid_amount bigint NOT NULL,
    paid_at     date
);

CREATE INDEX ON orders.orders (customer_id);
CREATE INDEX ON billing.invoices (customer_id);
CREATE INDEX ON shipping.shipments (order_id);
