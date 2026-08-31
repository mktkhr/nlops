// Command shipping は配送サービスのモック実装。
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

const name = "shipping"

func main() {
	addr := flag.String("addr", ":9104", "listen address")
	flag.Parse()

	ctx := context.Background()
	s, err := svc.New(ctx, name, svc.DSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "起動失敗:", err)
		os.Exit(1)
	}

	s.Handle("GET /carriers", func(ctx context.Context, _ authctx.Identity, r *http.Request) (any, error) {
		rows, err := svc.Rows(ctx, s.Pool, `SELECT carrier, name FROM shipping.carriers ORDER BY carrier`)
		if err != nil {
			return nil, err
		}
		return svc.ListOf("shipping", rows), nil
	})

	// 注文の現在の配送状況。まだ出荷されていない注文では 404 を返す。
	s.Handle("GET /orders/{order_id}/shipment", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("order_id", svc.P(r, "order_id"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		row, err := svc.Row(ctx, s.Pool,
			`SELECT shipment_id, order_id, status, carrier, shipped_at FROM shipping.shipments`+w.SQL(), w.Args()...)
		if err == svc.ErrNotFound {
			return nil, svc.NotFound("注文 %s の出荷はまだ登録されていないか参照権限がありません", svc.P(r, "order_id"))
		}
		return svc.Detail("shipping", row), err
	})

	// 配達予定日。現在地ではなく「いつ届くか」を答える。
	s.Handle("GET /orders/{order_id}/shipment/estimate", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("order_id", svc.P(r, "order_id"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		row, err := svc.Row(ctx, s.Pool,
			`SELECT order_id, estimated_at, confidence FROM shipping.shipments`+w.SQL(), w.Args()...)
		if err == svc.ErrNotFound {
			return nil, svc.NotFound("注文 %s の配達予定はまだ確定していないか参照権限がありません", svc.P(r, "order_id"))
		}
		return svc.Detail("shipping", row), err
	})

	s.Handle("GET /shipments", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("order_id", svc.Q(r, "order_id"))
		w.Eq("status", svc.Q(r, "status"))
		w.Eq("carrier", svc.Q(r, "carrier"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		rows, err := svc.Rows(ctx, s.Pool,
			`SELECT shipment_id, order_id, status, carrier FROM shipping.shipments`+
				w.SQL()+` ORDER BY shipment_id`, w.Args()...)
		if err != nil {
			return nil, err
		}
		return svc.ListOf("shipping", rows), nil
	})

	s.Handle("GET /shipments/{shipment_id}/tracking", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		sid := svc.P(r, "shipment_id")
		w := &svc.W{}
		w.Eq("shipment_id", sid)
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		if _, err := svc.Row(ctx, s.Pool, `SELECT shipment_id FROM shipping.shipments`+w.SQL(), w.Args()...); err != nil {
			if err == svc.ErrNotFound {
				return nil, svc.NotFound("出荷 %s は存在しないか参照権限がありません", sid)
			}
			return nil, err
		}
		rows, err := svc.Rows(ctx, s.Pool,
			`SELECT occurred_at, location, event FROM shipping.tracking_events
			 WHERE shipment_id = $1 ORDER BY seq`, sid)
		if err != nil {
			return nil, err
		}
		return svc.ListOf("shipping", rows), nil
	})

	if err := s.Run(*addr); err != nil {
		fmt.Fprintln(os.Stderr, "停止:", err)
		os.Exit(1)
	}
}
