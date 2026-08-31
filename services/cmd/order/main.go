// Command order は注文サービスのモック実装。
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/services/internal/svc"
)

const name = "order"

func main() {
	addr := flag.String("addr", ":9102", "listen address")
	flag.Parse()

	ctx := context.Background()
	s, err := svc.New(ctx, name, svc.DSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "起動失敗:", err)
		os.Exit(1)
	}

	s.Handle("GET /orders/summary", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		cid := svc.Q(r, "customer_id")
		period := svc.Q(r, "period")
		var rangeCond string
		switch period {
		case "THIS_MONTH":
			rangeCond = `ordered_at >= date_trunc('month', CURRENT_DATE)::date`
		case "LAST_MONTH":
			rangeCond = `ordered_at >= (date_trunc('month', CURRENT_DATE) - interval '1 month')::date
			             AND ordered_at < date_trunc('month', CURRENT_DATE)::date`
		case "THIS_YEAR":
			rangeCond = `ordered_at >= date_trunc('year', CURRENT_DATE)::date`
		default:
			return nil, fmt.Errorf("period は THIS_MONTH / LAST_MONTH / THIS_YEAR のいずれかです: %q", period)
		}
		w := &svc.W{}
		w.Eq("customer_id", cid)
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		w.Raw(rangeCond)
		w.Raw(`status <> 'CANCELLED'`)
		row, err := svc.Row(ctx, s.Pool,
			`SELECT count(*)::int AS order_count, COALESCE(sum(total_amount),0)::bigint AS total_amount
			 FROM orders.orders`+w.SQL(), w.Args()...)
		if err != nil {
			return nil, err
		}
		row["customer_id"] = cid
		row["period"] = period
		return svc.Detail("order", row), nil
	})

	s.Handle("GET /orders/count-by-status", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("customer_id", svc.Q(r, "customer_id"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		rows, err := svc.Rows(ctx, s.Pool,
			`SELECT status, count(*)::int AS count FROM orders.orders`+w.SQL()+
				` GROUP BY status ORDER BY status`, w.Args()...)
		if err != nil {
			return nil, err
		}
		return svc.ListOf("order", rows), nil
	})

	s.Handle("GET /orders", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("customer_id", svc.Q(r, "customer_id"))
		w.Eq("status", svc.Q(r, "status"))
		w.Gte("ordered_at", svc.Q(r, "ordered_from"))
		w.Lte("ordered_at", svc.Q(r, "ordered_to"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		rows, err := svc.Rows(ctx, s.Pool,
			`SELECT order_id, customer_id, status, ordered_at, total_amount FROM orders.orders`+
				w.SQL()+` ORDER BY ordered_at DESC, order_id DESC`, w.Args()...)
		if err != nil {
			return nil, err
		}
		return svc.ListOf("order", rows), nil
	})

	s.Handle("GET /orders/{order_id}", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("o.order_id", svc.P(r, "order_id"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("o.region", region)
		}
		row, err := svc.Row(ctx, s.Pool,
			`SELECT o.order_id, o.customer_id, o.status, o.ordered_at, o.total_amount,
			        (SELECT count(*)::int FROM orders.order_items i WHERE i.order_id = o.order_id) AS item_count
			 FROM orders.orders o`+w.SQL(), w.Args()...)
		if err == svc.ErrNotFound {
			return nil, svc.NotFound("注文 %s は存在しないか参照権限がありません", svc.P(r, "order_id"))
		}
		return svc.Detail("order", row), err
	})

	s.Handle("GET /orders/{order_id}/items", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		oid := svc.P(r, "order_id")
		w := &svc.W{}
		w.Eq("order_id", oid)
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		if _, err := svc.Row(ctx, s.Pool, `SELECT order_id FROM orders.orders`+w.SQL(), w.Args()...); err != nil {
			if err == svc.ErrNotFound {
				return nil, svc.NotFound("注文 %s は存在しないか参照権限がありません", oid)
			}
			return nil, err
		}
		rows, err := svc.Rows(ctx, s.Pool,
			`SELECT product_id, product_name, quantity, unit_price FROM orders.order_items
			 WHERE order_id = $1 ORDER BY line_no`, oid)
		if err != nil {
			return nil, err
		}
		return svc.ListOf("order", rows), nil
	})

	if err := s.Run(*addr); err != nil {
		fmt.Fprintln(os.Stderr, "停止:", err)
		os.Exit(1)
	}
}
