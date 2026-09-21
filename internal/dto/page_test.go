package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewPageNormalizesNilItems(t *testing.T) {
	p := NewPage[string](nil, 0, 20, 0)

	raw, err := json.Marshal(p)
	require.NoError(t, err)
	// nil-слайс сериализуется как null; клиенту нужен [].
	require.Contains(t, string(raw), `"items":[]`)
}

func TestPageArithmetic(t *testing.T) {
	cases := []struct {
		name                   string
		total, limit, offset   int
		pages, current         int
		hasPrev, hasNext       bool
		prevOffset, nextOffset int
	}{
		{"первая из трёх", 47, 20, 0, 3, 1, false, true, 0, 20},
		{"вторая из трёх", 47, 20, 20, 3, 2, true, true, 0, 40},
		{"последняя из трёх", 47, 20, 40, 3, 3, true, false, 20, 40},
		{"ровно одна страница", 20, 20, 0, 1, 1, false, false, 0, 0},
		{"пусто", 0, 20, 0, 0, 1, false, false, 0, 0},
		{"limit нулевой", 10, 0, 0, 0, 1, false, false, 0, 0},
		{"offset за пределами", 10, 20, 100, 1, 6, true, false, 80, 100},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPage([]int{}, tc.total, tc.limit, tc.offset)
			require.Equal(t, tc.pages, p.Pages(), "Pages")
			require.Equal(t, tc.current, p.CurrentPage(), "CurrentPage")
			require.Equal(t, tc.hasPrev, p.HasPrev(), "HasPrev")
			require.Equal(t, tc.hasNext, p.HasNext(), "HasNext")
			require.Equal(t, tc.prevOffset, p.PrevOffset(), "PrevOffset")
			require.Equal(t, tc.nextOffset, p.NextOffset(), "NextOffset")
		})
	}
}
