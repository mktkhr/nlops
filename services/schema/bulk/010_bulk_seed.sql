-- 実運用に近い規模のデータ。
--
-- 002_seed.sql の後に流す (make db-bulk)。compose の初期化では読み込まない。
--
-- 設計:
--   - 既存の C001-C006 / O-1001..O-1011 / P001-P006 はそのまま残す。
--     ゴールデンセット 170 件がこれらの ID に依存しているため。
--   - 追加分は ID が既存より後に並び (C10000 以降)、日付は既存より古い
--     (2024-01 〜 2026-07)。一覧は新しい順に並ぶので、既存データが先頭に残る。
--   - 姓を意図的に重複させる。「田中」で数百件が返る状態を作らないと、
--     Projection の切り詰めも同名の曖昧性も検証できない。
--   - **名は 002_seed.sql の 6 人と重ならないものを使う。**
--     20 姓 × 20 名 = 400 通りを 5,000 人へ割り当てるので、同姓同名は
--     バルク内で 12〜13 人ずつ発生する (曖昧性の検証にはこれが要る)。
--     ただし C001「田中太郎」等に同名を生やすと、氏名で 1 件に特定できる
--     前提のゴールデンケースが成立しなくなる。

-- ---------- 顧客 5,000 件 ----------
INSERT INTO customer.customers
SELECT
    'C' || (10000 + i)::text,
    (ARRAY['田中','佐藤','鈴木','高橋','伊藤','渡辺','山本','中村','小林','加藤',
           '吉田','山田','佐々木','山口','松本','井上','木村','林','斎藤','清水'])[1 + (i % 20)]
      || (ARRAY['彩','陽菜','蓮','颯太','美咲','結衣','翔','祐介','愛','大輔',
                'さくら','拓也','由美','聡','恵','剛','麻衣','亮','千尋','直樹'])[1 + ((i / 20) % 20)],
    'user' || i || '@' || CASE WHEN i % 2 = 0 THEN 'east' ELSE 'west' END || '.example.com',
    CASE WHEN i % 2 = 0 THEN '03-' ELSE '06-' END || lpad((3000 + i % 6000)::text, 4, '0') || '-' || lpad((i % 10000)::text, 4, '0'),
    CASE WHEN i % 2 = 0 THEN 'EAST' ELSE 'WEST' END,
    CASE WHEN i % 37 = 0 THEN 'SUSPENDED' WHEN i % 23 = 0 THEN 'INACTIVE' ELSE 'ACTIVE' END,
    CASE WHEN i % 2 = 0 THEN '東日本営業' ELSE '西日本営業' END,
    (ARRAY['A','B','C'])[1 + (i % 3)],
    ((i % 20) + 1) * 500000,
    DATE '2025-04-01' + ((i % 300) || ' days')::interval
FROM generate_series(1, 5000) AS i;

-- 窓口担当者。1 顧客あたり 1〜2 名。
INSERT INTO customer.contacts
SELECT
    'CT-' || (1000 + i)::text,
    'C' || (10000 + ((i - 1) / 2 + 1))::text,
    (ARRAY['山本','中村','小林','加藤','吉田'])[1 + (i % 5)] || (ARRAY['一','二','三','四','五'])[1 + ((i / 5) % 5)],
    (ARRAY['購買責任者','経理担当','代表','窓口','情報システム'])[1 + (i % 5)],
    'contact' || i || '@example.com'
FROM generate_series(1, 7000) AS i;

-- ---------- 商品 500 件 ----------
INSERT INTO inventory.products
SELECT
    'P' || (1000 + i)::text,
    (ARRAY['ワイヤレスマウス','メカニカルキーボード','モニタ','USBハブ','スタンド',
           'ウェブカメラ','ヘッドセット','ドッキングステーション','外付けSSD','電源アダプタ'])[1 + (i % 10)]
      || ' ' || (ARRAY['Pro','Lite','Max','Air','Plus','SE','X','Neo','Mini','Ultra'])[1 + ((i / 10) % 10)]
      || ' ' || (1000 + i)::text,
    (ARRAY['周辺機器','ディスプレイ','アクセサリ','ストレージ','電源'])[1 + (i % 5)],
    ((i % 40) + 1) * 1200,
    (i % 29 = 0)
FROM generate_series(1, 500) AS i;

-- 在庫は全商品 × 全倉庫。
INSERT INTO inventory.stock
SELECT p.product_id, w.warehouse_id,
       (abs(hashtext(p.product_id || w.warehouse_id)) % 300),
       (abs(hashtext(w.warehouse_id || p.product_id)) % 30)
FROM inventory.products p
CROSS JOIN inventory.warehouses w
WHERE length(p.product_id) >= 5;  -- 既存の P001..P006 (4文字) を除く

-- ---------- 注文 50,000 件 ----------
-- 追加分は既存より古い日付にする。一覧は新しい順なので既存が先頭に残る。
-- 状態も過去のものらしく DELIVERED 中心にして、「未発送」の検索結果を
-- 既存データが支配するようにしておく。
INSERT INTO orders.orders
SELECT
    'O-' || (50000 + i)::text,
    c.customer_id,
    c.region,
    CASE WHEN i % 50 = 0 THEN 'CANCELLED'
         WHEN i % 17 = 0 THEN 'SHIPPED'
         WHEN i % 97 = 0 THEN 'CONFIRMED'
         WHEN i % 89 = 0 THEN 'PLACED'
         ELSE 'DELIVERED' END,
    DATE '2024-01-01' + ((i % 940) || ' days')::interval,
    ((i % 60) + 1) * 3800
FROM generate_series(1, 50000) AS i
JOIN LATERAL (
    SELECT customer_id, region FROM customer.customers
    WHERE customer_id = 'C' || (10000 + 1 + (i % 5000))::text
) c ON true;

-- 注文明細。1 注文あたり 1〜3 行。
INSERT INTO orders.order_items
SELECT o.order_id, l.line_no, p.product_id, p.name,
       1 + (abs(hashtext(o.order_id || l.line_no::text)) % 8),
       p.unit_price
FROM orders.orders o
CROSS JOIN LATERAL generate_series(1, 1 + (abs(hashtext(o.order_id)) % 3)) AS l(line_no)
JOIN LATERAL (
    SELECT product_id, name, unit_price FROM inventory.products
    WHERE product_id = 'P' || (1000 + 1 + (abs(hashtext(o.order_id || l.line_no::text)) % 500))::text
) p ON true
WHERE length(o.order_id) >= 7;  -- 既存の O-1001 (6文字) を除く

-- ---------- 出荷 ----------
INSERT INTO shipping.shipments
SELECT
    'S-' || (50000 + row_number() OVER (ORDER BY o.order_id))::text,
    o.order_id, o.region,
    CASE WHEN o.status = 'DELIVERED' THEN 'DELIVERED'
         WHEN abs(hashtext(o.order_id)) % 20 = 0 THEN 'FAILED'
         ELSE 'IN_TRANSIT' END,
    (ARRAY['YAMATO','SAGAWA','JPPOST'])[1 + (abs(hashtext(o.order_id)) % 3)],
    o.ordered_at + interval '1 day',
    o.ordered_at + interval '4 days',
    (ARRAY['HIGH','MEDIUM','LOW'])[1 + (abs(hashtext(o.order_id)) % 3)]
FROM orders.orders o
WHERE length(o.order_id) >= 7 AND o.status IN ('SHIPPED', 'DELIVERED');

-- ---------- 請求 ----------
INSERT INTO billing.invoices
SELECT
    'INV-' || (50000 + row_number() OVER (ORDER BY o.order_id))::text,
    o.customer_id, o.order_id, o.region,
    CASE WHEN abs(hashtext(o.order_id)) % 11 = 0 THEN 'OVERDUE'
         WHEN abs(hashtext(o.order_id)) % 7 = 0 THEN 'ISSUED'
         WHEN abs(hashtext(o.order_id)) % 101 = 0 THEN 'VOID'
         ELSE 'PAID' END,
    o.ordered_at + interval '1 day',
    o.ordered_at + interval '31 days',
    o.total_amount
FROM orders.orders o
WHERE length(o.order_id) >= 7 AND o.status <> 'CANCELLED';

INSERT INTO billing.payments
SELECT i.invoice_id,
       CASE i.status WHEN 'PAID' THEN i.amount
                     WHEN 'OVERDUE' THEN (i.amount / 3)
                     ELSE 0 END,
       CASE WHEN i.status = 'PAID' THEN i.due_at - interval '3 days' ELSE NULL END
FROM billing.invoices i
WHERE length(i.invoice_id) >= 9 AND abs(hashtext(i.invoice_id)) % 4 <> 0;  -- 既存の INV-2001 (8文字) を除く

ANALYZE;
