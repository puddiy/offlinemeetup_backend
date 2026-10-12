package admin

import (
	"testing"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/stretchr/testify/require"
)

func seriesOf(start string, counts ...int) dto.DailySeries {
	s := dto.DailySeries{}
	d := mustDate(start)
	for i, c := range counts {
		s.Points = append(s.Points, dto.DayPoint{Date: d.AddDate(0, 0, i).Format("2006-01-02"), Count: c})
		s.Total += c
		s.Max = max(s.Max, c)
	}
	return s
}

func TestBarChartScalesToMax(t *testing.T) {
	c := newBarChart(seriesOf("2026-10-08", 0, 5, 10))

	require.False(t, c.Empty)
	require.Len(t, c.Bars, 3)

	top := c.Bars[2]
	require.Equal(t, chartHeight, top.H, "максимум — во всю высоту")
	require.Equal(t, 0.0, top.Y)

	mid := c.Bars[1]
	require.Equal(t, chartHeight/2, mid.H)
	require.Equal(t, chartHeight/2, mid.Y, "столбец стоит на базовой линии")

	require.Equal(t, 0.0, c.Bars[0].H, "ноль — без столбца")

	// Три колонки по 200 единиц, столбец — 80% колонки по центру.
	require.Equal(t, 400.0, top.SlotX)
	require.Equal(t, 200.0, top.SlotW)
	require.Equal(t, 420.0, top.X)
	require.Equal(t, 160.0, top.W)
}

func TestBarChartTinyValueStaysVisible(t *testing.T) {
	c := newBarChart(seriesOf("2026-10-09", 1, 1000))
	require.Equal(t, chartMinBar, c.Bars[0].H, "1 из 1000 — не ноль, столбец обязан быть виден")
}

func TestBarChartAllZeroIsEmpty(t *testing.T) {
	c := newBarChart(seriesOf("2026-10-08", 0, 0, 0))
	require.True(t, c.Empty)
	for _, b := range c.Bars {
		require.Equal(t, 0.0, b.H)
	}

	require.True(t, newBarChart(dto.DailySeries{}).Empty, "пустой ряд не паникует")
}

func TestBarChartTicksEveryWeekFromToday(t *testing.T) {
	counts := make([]int, 30)
	c := newBarChart(seriesOf("2026-09-11", counts...))

	require.Len(t, c.Ticks, 30)
	for i, tick := range c.Ticks {
		if (29-i)%7 == 0 {
			require.NotEmpty(t, tick, "индекс %d", i)
		} else {
			require.Empty(t, tick, "индекс %d", i)
		}
	}
	require.Equal(t, "10.10", c.Ticks[29], "подпись у сегодняшнего дня")
	require.Equal(t, "03.10", c.Ticks[22])
}

func TestBarChartBarLabels(t *testing.T) {
	c := newBarChart(seriesOf("2026-10-09", 4, 7))
	require.Equal(t, "09.10", c.Bars[0].Label)
	require.Equal(t, 4, c.Bars[0].Count)
	require.Equal(t, "10.10", c.Bars[1].Label)
}

func mustDate(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}
