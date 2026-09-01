// Command billing は請求サービスのモック実装。
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

const name = "billing"

func main() {
	addr := flag.String("addr", ":9105", "listen address")
	flag.Parse()

	ctx := context.Background()
	s, err := svc.New(ctx, name, svc.DSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "起動失敗:", err)
		os.Exit(1)
	}

	// 未払い一覧。ISSUED と OVERDUE をまとめて返す。
	s.Handle("GET /invoices/unpaid", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("customer_id", svc.Q(r, "customer_id"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		w.Raw(`status IN ('ISSUED','OVERDUE')`)
		return svc.ListPage(ctx, s.Pool, name,
			"invoice_id, customer_id, status, due_at, amount",
			"FROM billing.invoices"+w.SQL(),
			"ORDER BY due_at, invoice_id", svc.Pg(r), w.Args()...)
	})

	s.Handle("GET /invoices", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("customer_id", svc.Q(r, "customer_id"))
		w.Eq("status", svc.Q(r, "status"))
		w.Gte("issued_at", svc.Q(r, "issued_from"))
		w.Lte("issued_at", svc.Q(r, "issued_to"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		return svc.ListPage(ctx, s.Pool, name,
			"invoice_id, customer_id, status, issued_at, due_at, amount",
			"FROM billing.invoices"+w.SQL(),
			"ORDER BY issued_at DESC, invoice_id", svc.Pg(r), w.Args()...)
	})

	s.Handle("GET /invoices/{invoice_id}", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("invoice_id", svc.P(r, "invoice_id"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		row, err := svc.Row(ctx, s.Pool,
			`SELECT invoice_id, customer_id, order_id, status, issued_at, due_at, amount
			 FROM billing.invoices`+w.SQL(), w.Args()...)
		if err == svc.ErrNotFound {
			return nil, svc.NotFound("請求書 %s は存在しないか参照権限がありません", svc.P(r, "invoice_id"))
		}
		return svc.Detail("billing", row), err
	})

	s.Handle("GET /invoices/{invoice_id}/payment", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("i.invoice_id", svc.P(r, "invoice_id"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("i.region", region)
		}
		row, err := svc.Row(ctx, s.Pool,
			`SELECT i.invoice_id,
			        COALESCE(p.paid_amount, 0)::bigint AS paid_amount,
			        (i.amount - COALESCE(p.paid_amount, 0))::bigint AS remaining_amount,
			        p.paid_at
			 FROM billing.invoices i LEFT JOIN billing.payments p ON p.invoice_id = i.invoice_id`+w.SQL(), w.Args()...)
		if err == svc.ErrNotFound {
			return nil, svc.NotFound("請求書 %s は存在しないか参照権限がありません", svc.P(r, "invoice_id"))
		}
		return svc.Detail("billing", row), err
	})

	// 未払い残高の合計。与信限度額 (customer サービス) とは別物。
	s.Handle("GET /customers/{customer_id}/balance", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		cid := svc.P(r, "customer_id")
		w := &svc.W{}
		w.Eq("i.customer_id", cid)
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("i.region", region)
		}
		w.Raw(`i.status IN ('ISSUED','OVERDUE')`)
		row, err := svc.Row(ctx, s.Pool,
			`SELECT COALESCE(sum(i.amount - COALESCE(p.paid_amount,0)),0)::bigint AS outstanding_amount,
			        COALESCE(sum(CASE WHEN i.status='OVERDUE'
			                          THEN i.amount - COALESCE(p.paid_amount,0) ELSE 0 END),0)::bigint AS overdue_amount,
			        count(*)::int AS invoice_count
			 FROM billing.invoices i LEFT JOIN billing.payments p ON p.invoice_id = i.invoice_id`+w.SQL(), w.Args()...)
		if err != nil {
			return nil, err
		}
		row["customer_id"] = cid
		return svc.Detail("billing", row), nil
	})

	if err := s.Run(*addr); err != nil {
		fmt.Fprintln(os.Stderr, "停止:", err)
		os.Exit(1)
	}
}
