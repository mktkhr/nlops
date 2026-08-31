// Package authctx はユーザー識別情報をサービス間で伝播させる。
//
// 設計方針: LLM は認証情報を一切持たない。Executor がユーザー識別情報を
// HTTP ヘッダへ載せ、各サービスが自分の責務の範囲で絞り込むか 403 を返す。
// LLM が READ-only であっても、この伝播がなければ横断検索は権限を無視した
// 情報漏洩経路になるため、PoC 初日から通す。
package authctx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// Role はユーザーの役割。
type Role string

const (
	RoleAdmin     Role = "admin"
	RoleSales     Role = "sales"
	RoleWarehouse Role = "warehouse"
	RoleSupport   Role = "support"
)

// Access はあるサービスに対するアクセス範囲。
type Access string

const (
	AccessAll    Access = "all"    // 全件参照できる
	AccessRegion Access = "region" // 自分の担当地域のみ
	AccessDeny   Access = "deny"   // 参照できない
)

// Identity は 1 ユーザーの識別情報。
type Identity struct {
	UserID      string `json:"user_id"`
	Name        string `json:"name"`
	Role        Role   `json:"role"`
	Region      string `json:"region"`       // sales のみ有効
	WarehouseID string `json:"warehouse_id"` // warehouse のみ有効
}

// HTTP ヘッダ名。
const (
	HeaderUserID    = "X-Nlops-User-Id"
	HeaderRole      = "X-Nlops-Role"
	HeaderRegion    = "X-Nlops-Region"
	HeaderWarehouse = "X-Nlops-Warehouse-Id"
)

// Apply はリクエストへ識別情報を載せる。Executor が呼ぶ。
func (id Identity) Apply(r *http.Request) {
	r.Header.Set(HeaderUserID, id.UserID)
	r.Header.Set(HeaderRole, string(id.Role))
	r.Header.Set(HeaderRegion, id.Region)
	r.Header.Set(HeaderWarehouse, id.WarehouseID)
}

// FromRequest はリクエストから識別情報を取り出す。各サービスが呼ぶ。
func FromRequest(r *http.Request) (Identity, error) {
	id := Identity{
		UserID:      r.Header.Get(HeaderUserID),
		Role:        Role(r.Header.Get(HeaderRole)),
		Region:      r.Header.Get(HeaderRegion),
		WarehouseID: r.Header.Get(HeaderWarehouse),
	}
	if id.UserID == "" || id.Role == "" {
		return id, fmt.Errorf("認証情報がありません")
	}
	switch id.Role {
	case RoleAdmin, RoleSales, RoleWarehouse, RoleSupport:
	default:
		return id, fmt.Errorf("未知のロール: %s", id.Role)
	}
	if id.Role == RoleSales && id.Region == "" {
		return id, fmt.Errorf("sales ロールに region がありません")
	}
	return id, nil
}

// accessMatrix は roles.json の service_access と同じ内容をコードにも持つ。
// カタログ側とずれた場合に検知できるよう Roles.Verify で突き合わせる。
var accessMatrix = map[Role]map[string]Access{
	RoleAdmin:     {"customer": AccessAll, "order": AccessAll, "inventory": AccessAll, "shipping": AccessAll, "billing": AccessAll},
	RoleSales:     {"customer": AccessRegion, "order": AccessRegion, "inventory": AccessAll, "shipping": AccessRegion, "billing": AccessRegion},
	RoleWarehouse: {"customer": AccessDeny, "order": AccessDeny, "inventory": AccessAll, "shipping": AccessDeny, "billing": AccessDeny},
	RoleSupport:   {"customer": AccessAll, "order": AccessAll, "inventory": AccessAll, "shipping": AccessAll, "billing": AccessDeny},
}

// writeMatrix は更新操作の可否。参照より厳しくする。
// support は参照専用、sales は在庫を更新できない、warehouse は在庫だけ更新できる。
var writeMatrix = map[Role]map[string]bool{
	RoleAdmin:     {"customer": true, "order": true, "inventory": true, "shipping": true, "billing": true},
	RoleSales:     {"customer": true, "order": true},
	RoleWarehouse: {"inventory": true},
	RoleSupport:   {},
}

// CanWrite は指定サービスへの更新が許されるかを返す。
// 参照できることと更新できることは別に判定する。
func (id Identity) CanWrite(service string) bool {
	if id.AccessTo(service) == AccessDeny {
		return false
	}
	return writeMatrix[id.Role][service]
}

// AccessTo はこの識別情報が指定サービスに対して持つアクセス範囲を返す。
func (id Identity) AccessTo(service string) Access {
	m, ok := accessMatrix[id.Role]
	if !ok {
		return AccessDeny
	}
	a, ok := m[service]
	if !ok {
		return AccessDeny
	}
	return a
}

// RegionFilter は SQL の WHERE に足すべき地域制約を返す。
// 空文字なら制約なし。AccessDeny の場合は ok=false を返す。
func (id Identity) RegionFilter(service string) (region string, ok bool) {
	switch id.AccessTo(service) {
	case AccessAll:
		return "", true
	case AccessRegion:
		return id.Region, true
	default:
		return "", false
	}
}

// ---- カタログ側の定義 ----

// Directory は roles.json の内容。
type Directory struct {
	Users []Identity `json:"users"`
}

// LoadDirectory は roles.json からユーザー一覧を読み込む。
func LoadDirectory(path string) (*Directory, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ロール定義読み込み: %w", err)
	}
	var d Directory
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("ロール定義解析: %w", err)
	}
	return &d, nil
}

// Lookup はユーザー ID で識別情報を引く。
func (d *Directory) Lookup(userID string) (Identity, error) {
	for _, u := range d.Users {
		if u.UserID == userID {
			return u, nil
		}
	}
	return Identity{}, fmt.Errorf("未知のユーザー: %s", userID)
}
