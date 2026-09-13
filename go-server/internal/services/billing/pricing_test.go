package billing

import "testing"

func TestCostMicro(t *testing.T) {
	cases := []struct {
		name            string
		tokens          int
		pricePerMillion int64
		want            int64
	}{
		{"zero tokens", 0, 150_000, 0},
		{"zero price", 1000, 0, 0},
		{"negative", -5, 150_000, 0},
		{"exact", 1500, 150_000, 225},
		{"half rounds up", 3, 500_000, 2},
		{"just below half rounds down", 1, 499_999, 0},
		{"exactly half rounds up", 1, 500_000, 1},
		{"output rate", 500, 600_000, 300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := costMicro(tc.tokens, tc.pricePerMillion); got != tc.want {
				t.Fatalf("costMicro(%d, %d) = %d, want %d", tc.tokens, tc.pricePerMillion, got, tc.want)
			}
		})
	}
}
