package tokenizer

import "testing"

func TestEstimator(t *testing.T) {
	e := Estimator{}
	if e.Name() != "estimate/v1" {
		t.Fatal(e.Name())
	}
	for n, w := range []int64{0, 1, 1, 1, 1, 2, 2, 2, 2} {
		if got := e.Estimate(make([]byte, n)); got != w {
			t.Fatalf("%d bytes=%d want %d", n, got, w)
		}
	}
}
