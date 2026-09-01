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
		// BFF が注文一覧に顧客名を添えるとき、返ってきた注文の顧客だけを引く。
		// 顧客一覧の先頭 1 ページを取って突き合わせると、5,000 件では当たらない。
		w.In("customer_id", svc.QList(r, "customer_ids", svc.MaxLimit))
		w.Eq("status", svc.Q(r, "status"))
		w.Eq("region", svc.Q(r, "region"))
		if region, _ := id.RegionFilter(name); region != "" {
			w.Eq("region", region)
		}
		// 並べ替えられる列は限る。LLM は enum で縛っているが、
		// API は直接叩けるのでここでも検証する。
		order := svc.OrderBy(r, svc.Sortable{
			"customer_id_asc":  "customer_id ASC",
			"customer_id_desc": "customer_id DESC",
			"name_asc":         "name ASC",
			"name_desc":        "name DESC",
		}, "customer_id ASC", "customer_id")
		return svc.ListPage(ctx, s.Pool, name,
			"customer_id, name, region, status",
			"FROM customer.customers"+w.SQL(),
			order, svc.Pg(r), w.Args()...)
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
		return svc.ListPage(ctx, s.Pool, name,
			"contact_id, name, role, email",
			"FROM customer.contacts WHERE customer_id = $1",
			"ORDER BY contact_id", svc.Pg(r), cid)
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

	// 連絡先の更新。氏名・地域・与信は変更させない。
	s.Handle("PATCH /customers/{customer_id}/contact", func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error) {
		if err := svc.RequireWrite(id, name); err != nil {
			return nil, err
		}
		var in struct {
			Email string `json:"email"`
			Phone string `json:"phone"`
		}
		if err := svc.Body(r, &in); err != nil {
			return nil, err
		}
		if in.Email == "" && in.Phone == "" {
			return nil, svc.Conflict("email か phone のどちらかを指定してください")
		}
		cid := svc.P(r, "customer_id")
		if err := assertVisible(ctx, s, id, cid); err != nil {
			return nil, err
		}
		row, err := svc.Row(ctx, s.Pool,
			`UPDATE customer.customers
			 SET email = COALESCE(NULLIF($2,''), email),
			     phone = COALESCE(NULLIF($3,''), phone)
			 WHERE customer_id = $1
			 RETURNING customer_id, name, email, phone`, cid, in.Email, in.Phone)
		if err == svc.ErrNotFound {
			return nil, svc.NotFound("顧客 %s は存在しません", cid)
		}
		return svc.Detail(name, row), err
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
