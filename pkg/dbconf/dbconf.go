// Package dbconf は DB 接続文字列の解決を 1 か所に集める。
//
// 秘密情報を環境変数で渡さない。環境変数は docker inspect や /proc/<pid>/environ
// から読め、子プロセスへも自動的に引き継がれる。Docker secrets はファイルとして
// tmpfs にマウントされるので、そのファイルを読む経路を用意する。
package dbconf

import (
	"fmt"
	"os"
	"strings"
)

// DefaultDSN はローカル開発用の既定値。秘密情報ではない。
const DefaultDSN = "postgres://nlops:nlops@127.0.0.1:5432/nlops?sslmode=disable"

// DSN は接続文字列を次の優先順で解決する。
//
//  1. NLOPS_DSN_FILE が指すファイルの中身 (Docker secrets の想定)
//  2. NLOPS_DSN の値 (ローカル開発の利便のため)
//  3. DefaultDSN
//
// ファイルが指定されているのに読めない場合は、理由を stderr へ出して空文字を返す。
// 空の DSN のまま接続すると「unix socket に繋がらない」といった無関係な
// エラーになって原因が分からなくなるため、ここで何が起きたかを明示する。
func DSN() string {
	if path := os.Getenv("NLOPS_DSN_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "NLOPS_DSN_FILE %q を読めません: %v\n", path, err)
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	if v := os.Getenv("NLOPS_DSN"); v != "" {
		return v
	}
	return DefaultDSN
}
