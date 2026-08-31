package spi

import "testing"

func TestDeriveContract(t *testing.T) {
	cases := []struct {
		spi      string
		contract string
		wantErr  bool
	}{
		{"PM-ACME2026001-01", "ACME2026001", false},
		{"ACME2026001-07", "", true}, // 缺 PM- 前缀
		{"PM-ACME2026001", "", true}, // 缺序号
		{"PM-x-7", "x", false},            // 小写字母也允许
		{"PM-ABC_123-07", "", true},       // 下划线不允许
		{"", "", true},
	}
	for _, c := range cases {
		got, err := DeriveContract(c.spi)
		if c.wantErr {
			if err == nil {
				t.Errorf("DeriveContract(%q): want error", c.spi)
			}
			continue
		}
		if err != nil {
			t.Errorf("DeriveContract(%q): %v", c.spi, err)
			continue
		}
		if got != c.contract {
			t.Errorf("DeriveContract(%q) = %q, want %q", c.spi, got, c.contract)
		}
	}
}

func TestResolveContractPrefersExplicit(t *testing.T) {
	got, err := ResolveContract("CUSTOM", "PM-ACME2026001-01")
	if err != nil {
		t.Fatal(err)
	}
	if got != "CUSTOM" {
		t.Fatalf("got %q, want CUSTOM", got)
	}
	got, err = ResolveContract("", "PM-ACME2026001-01")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ACME2026001" {
		t.Fatalf("got %q", got)
	}
}

func TestValidateSPI(t *testing.T) {
	if err := ValidateSPI("PM-ACME2026001-01"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"bad", "PM-X-", "", "PM-ACME2026001-01x"} {
		if err := ValidateSPI(bad); err == nil {
			t.Errorf("ValidateSPI(%q): want error", bad)
		}
	}
}
