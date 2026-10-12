package admin

import (
	"math"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
)

// Размеры области графика в единицах viewBox. SVG растягивается по ширине
// карточки (preserveAspectRatio="none"), высота в CSS фиксирована — поэтому
// подписи оси живут в HTML под графиком, а не в SVG: растянутый текст
// читался бы криво.
const (
	chartWidth  = 600.0
	chartHeight = 140.0
	// chartMinBar — минимальная высота ненулевого столбца: 1 событие рядом
	// с тысячей иначе округлилось бы до нуля и выглядело бы как «ничего».
	chartMinBar = 2.0
	// chartTickEvery — шаг подписей оси, считая от сегодняшнего дня.
	chartTickEvery = 7
	// chartBarShare — доля колонки под столбец; остальное — зазор между
	// соседями (при 30 днях — 4 единицы).
	chartBarShare = 0.8
)

// BarChart — готовая к выводу геометрия одного графика.
type BarChart struct {
	Width  float64
	Height float64
	Bars   []Bar
	// Ticks — подпись под каждой колонкой; пустая строка — без подписи.
	// Длина равна len(Bars): ось в CSS — сетка из стольких же колонок.
	Ticks []string
	// Empty — событий за весь период нет: вместо графика выводится заглушка.
	Empty bool
}

// Bar — один день. Slot* — вся колонка (по ней ловится наведение и
// подсказка), X/Y/W/H — видимый столбец.
type Bar struct {
	SlotX, SlotW float64
	X, Y, W, H   float64
	Label        string
	Count        int
}

// newBarChart считает столбцы ряда. Высота пропорциональна s.Max.
func newBarChart(s dto.DailySeries) BarChart {
	c := BarChart{Width: chartWidth, Height: chartHeight, Empty: s.Max == 0}
	n := len(s.Points)
	if n == 0 {
		c.Empty = true
		return c
	}

	slot := chartWidth / float64(n)
	c.Bars = make([]Bar, n)
	c.Ticks = make([]string, n)
	for i, p := range s.Points {
		label := dayLabel(p.Date)

		h := 0.0
		if s.Max > 0 && p.Count > 0 {
			h = math.Round(float64(p.Count) / float64(s.Max) * chartHeight)
			h = max(h, chartMinBar)
		}

		c.Bars[i] = Bar{
			SlotX: round2(float64(i) * slot),
			SlotW: round2(slot),
			X:     round2(float64(i)*slot + slot*(1-chartBarShare)/2),
			W:     round2(slot * chartBarShare),
			Y:     chartHeight - h,
			H:     h,
			Label: label,
			Count: p.Count,
		}
		if (n-1-i)%chartTickEvery == 0 {
			c.Ticks[i] = label
		}
	}
	return c
}

// dayLabel — "2026-10-10" → "10.10". Нераспознанная дата выводится как есть:
// график не должен падать из-за формата одной точки.
func dayLabel(date string) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return d.Format("02.01")
}

// round2 — две цифры после запятой: атрибутам SVG не нужны хвосты вида
// 6.666666666666667.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
