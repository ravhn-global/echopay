// Package money treats all amounts as kobo (1/100 of a Naira). Never floats.
package money

import (
	"fmt"
	"strconv"
)

// Kobo is the smallest unit. Use int64 end-to-end.
type Kobo int64

// FromNaira converts a whole Naira amount to kobo.
func FromNaira(naira int64) Kobo {
	return Kobo(naira * 100)
}

// ToNairaString formats kobo as "₦1,500.00".
func (k Kobo) ToNairaString() string {
	negative := k < 0
	v := k
	if negative {
		v = -v
	}
	naira := int64(v) / 100
	cents := int64(v) % 100

	whole := withCommas(naira)
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s₦%s.%02d", sign, whole, cents)
}

// String returns the raw integer kobo value as a string (for logging/refs).
func (k Kobo) String() string {
	return strconv.FormatInt(int64(k), 10)
}

func withCommas(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	rem := len(s) % 3
	if rem > 0 {
		out = append(out, s[:rem]...)
		if len(s) > rem {
			out = append(out, ',')
		}
	}
	for i := rem; i < len(s); i += 3 {
		out = append(out, s[i:i+3]...)
		if i+3 < len(s) {
			out = append(out, ',')
		}
	}
	return string(out)
}
