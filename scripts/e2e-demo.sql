-- Demo data for the PostgreSQL end-to-end scenario (GOTABLE_E2E_PG):
--   psql "$GOTABLE_E2E_PG" -v ON_ERROR_STOP=1 -f scripts/e2e-demo.sql
CREATE TABLE customers (id serial PRIMARY KEY, name text NOT NULL, city text);
INSERT INTO customers (name, city)
SELECT 'Customer ' || i, (ARRAY['Berlin', 'Paris', NULL])[1 + i % 3] FROM generate_series(1, 420) i;
CREATE TABLE orders (id serial PRIMARY KEY, customer_id int REFERENCES customers (id), total numeric(10, 2));
INSERT INTO orders (customer_id, total) SELECT 1 + i % 420, i FROM generate_series(1, 1500) i;
CREATE INDEX orders_customer_idx ON orders (customer_id);
CREATE SCHEMA reporting;
CREATE TABLE reporting.monthly (month date, revenue numeric);
CREATE DATABASE analytics;
