-- Demo data for the README screenshots (frontend/e2e/screenshots.spec.ts):
-- a "shop" database with customers, orders, products, a view, and a second
-- schema. Load it into an empty database:
--   psql "postgres://postgres:pw@127.0.0.1:5432/shop" -v ON_ERROR_STOP=1 -f scripts/screenshot-demo.sql
CREATE TABLE customers (
  id serial PRIMARY KEY,
  name text NOT NULL,
  email text NOT NULL,
  city text,
  balance numeric(10, 2) NOT NULL DEFAULT 0,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO customers (name, email, city, balance, active, created_at)
SELECT 'Customer ' || i, 'c' || i || '@example.com',
  (ARRAY['Paris', 'Rome', 'Lagos', 'Tokyo', NULL, 'Berlin'])[1 + i % 6],
  round(((i * 318.01) % 1800 + 100)::numeric, 2), i % 7 <> 0,
  now() - make_interval(hours => i * 5)
FROM generate_series(1, 420) i;

CREATE TABLE products (
  id serial PRIMARY KEY,
  name text NOT NULL,
  price numeric(10, 2) NOT NULL,
  stock int NOT NULL DEFAULT 0
);
INSERT INTO products (name, price, stock)
SELECT 'Product ' || i, round((i * 7.5 + 4.99)::numeric, 2), (i * 13) % 90 FROM generate_series(1, 60) i;

CREATE TABLE orders (
  id serial PRIMARY KEY,
  customer_id int NOT NULL REFERENCES customers (id),
  product_id int REFERENCES products (id),
  total numeric(10, 2) NOT NULL,
  status text NOT NULL DEFAULT 'open',
  created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO orders (customer_id, product_id, total, status, created_at)
SELECT 1 + (i * 7) % 420, 1 + i % 60, round(((i * 47.3) % 900 + 20)::numeric, 2),
  (ARRAY['open', 'paid', 'shipped'])[1 + i % 3], now() - make_interval(hours => i * 2)
FROM generate_series(1, 2000) i;
CREATE INDEX orders_customer_idx ON orders (customer_id);

CREATE VIEW top_customers AS
SELECT c.id, c.name, c.city, sum(o.total) AS revenue
FROM customers c JOIN orders o ON o.customer_id = c.id
GROUP BY c.id ORDER BY revenue DESC LIMIT 20;

CREATE SCHEMA analytics;
CREATE TABLE analytics.daily_revenue (day date PRIMARY KEY, revenue numeric(12, 2));
