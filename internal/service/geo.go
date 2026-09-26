package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
)

const (
	daDataSuggestURL   = "https://suggestions.dadata.ru/suggestions/api/4_1/rs/suggest/address"
	daDataGeoLocateURL = "https://suggestions.dadata.ru/suggestions/api/4_1/rs/geolocate/address"
)

type GeoService struct {
	apiKey string
	client *http.Client
}

func NewGeoService(apiKey string) *GeoService {
	return &GeoService{apiKey: apiKey, client: &http.Client{Timeout: 5 * time.Second}}
}

func (s *GeoService) SuggestAddress(ctx context.Context, query string) ([]dto.AddressSuggestion, error) {
	reqBody := map[string]any{
		"query": query,
		"count": 10,
	}

	return s.sendRequest(ctx, daDataSuggestURL, reqBody)
}

func (s *GeoService) SuggestByGeo(ctx context.Context, lat, lon float64) ([]dto.AddressSuggestion, error) {
	reqBody := map[string]any{
		"lat":           lat,
		"lon":           lon,
		"count":         5,
		"radius_meters": 100,
	}
	return s.sendRequest(ctx, daDataGeoLocateURL, reqBody)
}

func (s *GeoService) sendRequest(ctx context.Context, url string, bodyData any) ([]dto.AddressSuggestion, error) {
	jsonBody, _ := json.Marshal(bodyData)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+s.apiKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dadata request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dadata returned status: %d", resp.StatusCode)
	}

	var daDataResp dto.DaDataResponse

	if err := json.NewDecoder(resp.Body).Decode(&daDataResp); err != nil {
		return nil, fmt.Errorf("failed to decode dadata response: %w", err)
	}

	result := make([]dto.AddressSuggestion, 0, len(daDataResp.Suggestions))
	for _, item := range daDataResp.Suggestions {
		lat, _ := strconv.ParseFloat(item.Data.GeoLat, 64)
		lon, _ := strconv.ParseFloat(item.Data.GeoLon, 64)

		result = append(result, dto.AddressSuggestion{
			Value: item.Value,
			Lat:   lat,
			Lon:   lon,
		})
	}
	return result, nil
}
