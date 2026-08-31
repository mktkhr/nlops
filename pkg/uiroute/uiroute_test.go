package uiroute

import "testing"

func testRoute() Route {
	return Route{
		Path: "/orders",
		Filters: map[string]Filter{
			"status":        {Type: "string", Enum: []string{"PLACED", "SHIPPED"}},
			"customer_name": {Type: "string"},
		},
	}
}

func TestSanitize(t *testing.T) {
	r := testRoute()
	tests := []struct {
		name string
		in   map[string]string
		want map[string]string
	}{
		{
			name: "定義済みのフィルタは通す",
			in:   map[string]string{"status": "PLACED", "customer_name": "田中"},
			want: map[string]string{"status": "PLACED", "customer_name": "田中"},
		},
		{
			name: "定義外のフィルタは落とす",
			in:   map[string]string{"status": "PLACED", "secret": "x"},
			want: map[string]string{"status": "PLACED"},
		},
		{
			name: "enum 外の値は落とす",
			in:   map[string]string{"status": "UNKNOWN", "customer_name": "田中"},
			want: map[string]string{"customer_name": "田中"},
		},
		{
			name: "空文字は落とす",
			in:   map[string]string{"status": "", "customer_name": "   "},
			want: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Sanitize(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("Sanitize(%v) = %v, 期待 %v", tt.in, got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("キー %s: %q, 期待 %q", k, got[k], v)
				}
			}
		})
	}
}

func TestFilterNamesIsStable(t *testing.T) {
	// prompt prefix を安定させるため、順序は map の iteration 順に依存してはいけない。
	r := testRoute()
	first := r.FilterNames()
	for range 20 {
		got := r.FilterNames()
		for i := range first {
			if got[i] != first[i] {
				t.Fatalf("FilterNames の順序が揺れた: %v と %v", first, got)
			}
		}
	}
	if first[0] != "customer_name" || first[1] != "status" {
		t.Errorf("昇順であるべき: %v", first)
	}
}

func TestByPath(t *testing.T) {
	c := &Catalog{Routes: []Route{testRoute()}}
	if _, ok := c.ByPath("/orders"); !ok {
		t.Error("定義済みの画面が引けない")
	}
	if _, ok := c.ByPath("/admin"); ok {
		t.Error("定義外の画面が引けてしまった")
	}
	if paths := c.Paths(); len(paths) != 1 || paths[0] != "/orders" {
		t.Errorf("Paths() = %v", paths)
	}
}
