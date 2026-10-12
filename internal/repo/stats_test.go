package repo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Текст запроса ряда выбирается из белого списка и никогда не собирается
// из аргумента: у каждого известного ряда ровно два плейсхолдера (пояс и
// начало окна) — в таком порядке их передаёт DailyCounts.
func TestStatsSeriesSQLCoversAllSeries(t *testing.T) {
	for _, s := range []StatsSeries{StatsSeriesRegistrations, StatsSeriesMeetups, StatsSeriesMessages} {
		q, ok := statsSeriesSQL[s]
		require.True(t, ok, "нет SQL для ряда %q", s)
		require.Equal(t, 2, strings.Count(q, "?"), "ряд %q", s)
	}
}

// Неизвестный ряд отбивается до похода в БД (db здесь nil — дошли бы до
// него, тест упал бы паникой).
func TestDailyCountsRejectsUnknownSeries(t *testing.T) {
	r := &StatsRepo{}
	_, err := r.DailyCounts(context.Background(), StatsSeries("users; DROP TABLE users"), time.Now().UTC(), "UTC")
	require.ErrorIs(t, err, ErrUnknownStatsSeries)
}
