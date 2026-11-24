package mathutil

import "testing"

func TestPercent(t *testing.T) {
	cases := []struct {
		part, whole int
		want        float64
	}{
		{1, 8, 12.5},
		{2, 3, 66.7},
		{150, 200, 75.0},
		{1, 3, 33.3},
	}
	for _, c := range cases {
		if got := Percent(c.part, c.whole); got != c.want {
			t.Errorf("Percent(%d, %d) = %v, want %v", c.part, c.whole, got, c.want)
		}
	}
}

func TestMean(t *testing.T) {
	if got := Mean([]int{1, 2}); got != 1.5 {
		t.Errorf("Mean([1,2]) = %v, want 1.5", got)
	}
	if got := Mean([]int{2, 3, 7}); got != 4.0 {
		t.Errorf("Mean([2,3,7]) = %v, want 4", got)
	}
	if got := Mean(nil); got != 0 {
		t.Errorf("Mean(nil) = %v, want 0", got)
	}
}
