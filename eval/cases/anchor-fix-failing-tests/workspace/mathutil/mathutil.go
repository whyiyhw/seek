package mathutil

// Percent returns the percentage that part is of whole, rounded to
// exactly one decimal place.
func Percent(part, whole int) float64 {
	return float64(part / whole * 100)
}

// Mean returns the arithmetic mean of xs (0 for empty), rounded to one
// decimal place.
func Mean(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0
	for _, x := range xs {
		sum += x
	}
	return float64(sum / len(xs))
}
