// Command inventory は在庫サービスのモック実装。
// 在庫は全ロールが参照できるため地域による絞り込みは行わない。
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

const name = "inventory"

func main() {
	addr := flag.String("addr", ":9103", "listen address")
	flag.Parse()

	ctx := context.Background()
	s, err := svc.New(ctx, name, svc.DSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "起動失敗:", err)
		os.Exit(1)
	}

	s.Handle("GET /products", func(ctx context.Context, _ authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Like("name", svc.Q(r, "keyword"))
		w.Like("category", svc.Q(r, "category"))
		order := svc.OrderBy(r, svc.Sortable{
			"unit_price_asc":  {Col: "unit_price", Type: "bigint"},
			"unit_price_desc": {Col: "unit_price", Desc: true, Type: "bigint"},
			"name_asc":        {Col: "name", Type: "text"},
			"name_desc":       {Col: "name", Desc: true, Type: "text"},
		}, svc.Sort{Col: "product_id", Type: "text"}, "product_id", "text")
		return svc.ListPage(ctx, s.Pool, name,
			"product_id, name, category, unit_price",
			"FROM inventory.products"+w.SQL(),
			order, svc.Pg(r), w.Args()...)
	})

	s.Handle("GET /products/{product_id}", func(ctx context.Context, _ authctx.Identity, r *http.Request) (any, error) {
		row, err := svc.Row(ctx, s.Pool,
			`SELECT product_id, name, category, unit_price, discontinued
			 FROM inventory.products WHERE product_id = $1`, svc.P(r, "product_id"))
		if err == svc.ErrNotFound {
			return nil, svc.NotFound("商品 %s は存在しません", svc.P(r, "product_id"))
		}
		return svc.Detail("inventory", row), err
	})

	s.Handle("GET /products/{product_id}/stock", func(ctx context.Context, _ authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("s.product_id", svc.P(r, "product_id"))
		w.Eq("s.warehouse_id", svc.Q(r, "warehouse_id"))
		rows, err := svc.Rows(ctx, s.Pool,
			`SELECT s.warehouse_id, wh.name AS warehouse_name, s.quantity, s.reserved
			 FROM inventory.stock s JOIN inventory.warehouses wh ON wh.warehouse_id = s.warehouse_id`+
				w.SQL()+` ORDER BY s.warehouse_id`, w.Args()...)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, svc.NotFound("商品 %s の在庫情報がありません", svc.P(r, "product_id"))
		}
		return svc.ListOf("inventory", rows), nil
	})

	s.Handle("GET /warehouses", func(ctx context.Context, _ authctx.Identity, r *http.Request) (any, error) {
		rows, err := svc.Rows(ctx, s.Pool,
			`SELECT warehouse_id, name, region FROM inventory.warehouses ORDER BY warehouse_id`)
		if err != nil {
			return nil, err
		}
		return svc.ListOf("inventory", rows), nil
	})

	s.Handle("GET /stock/low", func(ctx context.Context, _ authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		// below (未満) と threshold (以下) は境界が違う。
		// 「5 個を下回る」を「5 以下」で代用すると 1 件ずれるので兼ねない。
		if v := svc.Q(r, "below"); v != "" {
			w.Lt("s.quantity", svc.QInt(r, "below", 0))
		} else {
			w.Lte("s.quantity", svc.QInt(r, "threshold", 10))
		}
		w.Eq("s.warehouse_id", svc.Q(r, "warehouse_id"))
		w.Like("p.name", svc.Q(r, "product_name"))
		order := svc.OrderBy(r, svc.Sortable{
			"quantity_asc":  {Col: "s.quantity", Type: "int"},
			"quantity_desc": {Col: "s.quantity", Desc: true, Type: "int"},
		}, svc.Sort{Col: "s.quantity", Type: "int"}, "s.product_id", "text")
		return svc.ListPage(ctx, s.Pool, name,
			"s.product_id, p.name AS product_name, s.warehouse_id, s.quantity",
			"FROM inventory.stock s JOIN inventory.products p ON p.product_id = s.product_id"+w.SQL(),
			order, svc.Pg(r), w.Args()...)
	})

	// 在庫数の調整。増減ではなく絶対値で受ける。
	s.Handle("PATCH /products/{product_id}/stock", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		if err := svc.RequireWrite(id, name); err != nil {
			return nil, err
		}
		var in struct {
			WarehouseID string `json:"warehouse_id"`
			Quantity    *int   `json:"quantity"`
		}
		if err := svc.Body(r, &in); err != nil {
			return nil, err
		}
		if in.WarehouseID == "" || in.Quantity == nil {
			return nil, svc.Conflict("warehouse_id と quantity は必須です")
		}
		if *in.Quantity < 0 {
			return nil, svc.Conflict("在庫数に負の値は指定できません")
		}
		pid := svc.P(r, "product_id")
		row, err := svc.Row(ctx, s.Pool,
			`UPDATE inventory.stock SET quantity = $3
			 WHERE product_id = $1 AND warehouse_id = $2
			 RETURNING product_id, warehouse_id, quantity, reserved`, pid, in.WarehouseID, *in.Quantity)
		if err == svc.ErrNotFound {
			return nil, svc.NotFound("商品 %s の %s における在庫がありません", pid, in.WarehouseID)
		}
		return svc.Detail(name, row), err
	})

	if err := s.Run(*addr); err != nil {
		fmt.Fprintln(os.Stderr, "停止:", err)
		os.Exit(1)
	}
}
