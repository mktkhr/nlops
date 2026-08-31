// Command customer は顧客サービスのモック実装。
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

const name = "customer"

func main() {
	addr := flag.String("addr", ":9101", "listen address")
	flag.Parse()

	ctx := context.Background()
	s, err := svc.New(ctx, name, svc.DSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "起動失敗:", err)
		os.Exit(1)
	}

	// 顧客検索。sales ロールは自地域の顧客しか見えない。
	s.Handle("GET /customers", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Like("name", svc.Q(r, "name"))
		w.Like("email", svc.Q(r, "email"))
		w.Eq("status", svc.Q(r, "status"))
		w.Eq("region", svc.Q(r, "region"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		rows, err := svc.Rows(ctx, s.Pool,
			`SELECT customer_id, name, region, status FROM customer.customers`+
				w.SQL()+` ORDER BY customer_id`, w.Args()...)
		if err != nil {
			return nil, err
		}
		return svc.ListOf("customer", rows), nil
	})

	s.Handle("GET /customers/{customer_id}", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("customer_id", svc.P(r, "customer_id"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		row, err := svc.Row(ctx, s.Pool,
			`SELECT customer_id, name, email, phone, region, status, sales_rep, credit_rank
			 FROM customer.customers`+w.SQL(), w.Args()...)
		if err == svc.ErrNotFound {
			return nil, svc.NotFound("顧客 %s は存在しないか参照権限がありません", svc.P(r, "customer_id"))
		}
		return svc.Detail("customer", row), err
	})

	s.Handle("GET /customers/{customer_id}/contacts", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		cid := svc.P(r, "customer_id")
		if err := assertVisible(ctx, s, id, cid); err != nil {
			return nil, err
		}
		rows, err := svc.Rows(ctx, s.Pool,
			`SELECT contact_id, name, role, email FROM customer.contacts
			 WHERE customer_id = $1 ORDER BY contact_id`, cid)
		if err != nil {
			return nil, err
		}
		return svc.ListOf("customer", rows), nil
	})

	// 与信「区分」と「限度額」。実際の未払い残高は billing の責務。
	s.Handle("GET /customers/{customer_id}/credit", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		w := &svc.W{}
		w.Eq("customer_id", svc.P(r, "customer_id"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		row, err := svc.Row(ctx, s.Pool,
			`SELECT customer_id, credit_rank, credit_limit, reviewed_at FROM customer.customers`+w.SQL(), w.Args()...)
		if err == svc.ErrNotFound {
			return nil, svc.NotFound("顧客 %s は存在しないか参照権限がありません", svc.P(r, "customer_id"))
		}
		return svc.Detail("customer", row), err
	})

	if err := s.Run(*addr); err != nil {
		fmt.Fprintln(os.Stderr, "停止:", err)
		os.Exit(1)
	}
}

// assertVisible は指定顧客がこのユーザーから見えるかを確認する。
func assertVisible(ctx context.Context, s *svc.Server, id authctx.Identity, customerID string) error {
	w := &svc.W{}
	w.Eq("customer_id", customerID)
	if region, _ := id.RegionFilter(name); region != "" {
		w.Eq("region", region)
	}
	if _, err := svc.Row(ctx, s.Pool, `SELECT customer_id FROM customer.customers`+w.SQL(), w.Args()...); err != nil {
		if err == svc.ErrNotFound {
			return svc.NotFound("顧客 %s は存在しないか参照権限がありません", customerID)
		}
		return err
	}
	return nil
}
