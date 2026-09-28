package repo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUniqueIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []int64
		want []int64
	}{
		{"nil", nil, nil},
		{"empty", []int64{}, []int64{}},
		{"no dups", []int64{3, 1, 2}, []int64{3, 1, 2}},
		{"dups keep first order", []int64{3, 1, 3, 2, 1}, []int64{3, 1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, uniqueIDs(tc.in))
		})
	}
}
