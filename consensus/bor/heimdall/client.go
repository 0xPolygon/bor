package heimdall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/checkpoint"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/log"
)

var (
	// ErrShutdownDetected is returned if a shutdown was detected
	ErrShutdownDetected      = errors.New("shutdown detected")
	ErrNoResponse            = errors.New("got a nil response")
	ErrNotSuccessfulResponse = errors.New("error while fetching data from Heimdall")
)

const (
	stateFetchLimit    = 50
	apiHeimdallTimeout = 5 * time.Second
	retryCall          = 5 * time.Second
)

type StateSyncEventsResponse struct {
	Height string                       `json:"height"`
	Result []*clerk.EventRecordWithTime `json:"result"`
}

type SpanResponse struct {
	Height string            `json:"height"`
	Result span.HeimdallSpan `json:"result"`
}

type HeimdallClient struct {
	urlString string
	client    http.Client
	closeCh   chan struct{}
}

func NewHeimdallClient(urlString string) *HeimdallClient {
	return &HeimdallClient{
		urlString: urlString,
		client: http.Client{
			Timeout: apiHeimdallTimeout,
		},
		closeCh: make(chan struct{}),
	}
}

const (
	fetchStateSyncEventsFormat = "from-id=%d&to-time=%d&limit=%d"
	fetchStateSyncEventsPath   = "clerk/event-record/list"
	fetchLatestCheckpoint      = "/checkpoints/latest"

	fetchSpanFormat = "bor/span/%d"
)

func (h *HeimdallClient) StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
	eventRecords := make([]*clerk.EventRecordWithTime, 0)

	for {
		url, err := stateSyncURL(h.urlString, fromID, to)
		if err != nil {
			return nil, err
		}

		log.Info("Fetching state sync events", "queryParams", url.RawQuery)

		response, err := FetchWithRetry[StateSyncEventsResponse](ctx, h.client, url, h.closeCh)
		if err != nil {
			return nil, err
		}

		if response == nil || response.Result == nil {
			// status 204
			break
		}

		eventRecords = append(eventRecords, response.Result...)

		if len(response.Result) < stateFetchLimit {
			break
		}

		fromID += uint64(stateFetchLimit)
	}

	sort.SliceStable(eventRecords, func(i, j int) bool {
		return eventRecords[i].ID < eventRecords[j].ID
	})

	return eventRecords, nil
}

func (h *HeimdallClient) Span(ctx context.Context, spanID uint64) (*span.HeimdallSpan, error) {
	url, err := spanURL(h.urlString, spanID)
	if err != nil {
		return nil, err
	}

	response, err := FetchWithRetry[SpanResponse](ctx, h.client, url, h.closeCh)
	if err != nil {
		return nil, err
	}

	return &response.Result, nil
}

// FetchLatestCheckpoint fetches the latest bor submitted checkpoint from heimdall
func (h *HeimdallClient) FetchLatestCheckpoint(ctx context.Context) (*checkpoint.Checkpoint, error) {
	url, err := latestCheckpointURL(h.urlString)
	if err != nil {
		return nil, err
	}

	response, err := FetchWithRetry[checkpoint.CheckpointResponse](ctx, h.client, url, h.closeCh)
	if err != nil {
		return nil, err
	}

	return &response.Result, nil
}

// FetchWithRetry returns data from heimdall with retry
func FetchWithRetry[T any](ctx context.Context, client http.Client, url *url.URL, closeCh chan struct{}) (*T, error) {
	// request data once
	result, err := Fetch[T](ctx, client, url)
	if err == nil {
		return result, nil
	}

	// ignore or log the error

	// create a new ticker for retrying the request
	ticker := time.NewTicker(retryCall)
	defer ticker.Stop()

	// attempt counter
	attempt := 1

	for {
		log.Info("Retrying again in 5 seconds to fetch data from Heimdall", "path", url.Path, "attempt", attempt)

		attempt++

		select {
		case <-ctx.Done():
			log.Debug("Shutdown detected, terminating request")

			return nil, ctx.Err()
		case <-closeCh:
			log.Debug("Shutdown detected, terminating request")

			return nil, ErrShutdownDetected
		case <-ticker.C:
			result, err = Fetch[T](ctx, client, url)

			// ignore or log the error
			if err == nil {
				return result, nil
			}
		}
	}
}

// Fetch returns data from heimdall
func Fetch[T any](ctx context.Context, client http.Client, url *url.URL) (*T, error) {
	result := new(T)

	body, err := internalFetchWithTimeout(ctx, client, url)

	if body == nil {
		return nil, ErrNoResponse
	}

	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(body, result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func spanURL(urlString string, spanID uint64) (*url.URL, error) {
	return makeURL(urlString, fmt.Sprintf(fetchSpanFormat, spanID), "")
}

func stateSyncURL(urlString string, fromID uint64, to int64) (*url.URL, error) {
	queryParams := fmt.Sprintf(fetchStateSyncEventsFormat, fromID, to, stateFetchLimit)

	return makeURL(urlString, fetchStateSyncEventsPath, queryParams)
}

func latestCheckpointURL(urlString string) (*url.URL, error) {
	return makeURL(urlString, fetchLatestCheckpoint, "")
}

func makeURL(urlString, rawPath, rawQuery string) (*url.URL, error) {
	u, err := url.Parse(urlString)
	if err != nil {
		return nil, err
	}

	u.Path = rawPath
	u.RawQuery = rawQuery

	return u, err
}

// internal fetch method
func internalFetch(ctx context.Context, client http.Client, u *url.URL) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()

	// check status code
	if res.StatusCode != 200 && res.StatusCode != 204 {
		return nil, fmt.Errorf("%w: response code %d", ErrNotSuccessfulResponse, res.StatusCode)
	}

	// unmarshall data from buffer
	if res.StatusCode == 204 {
		return nil, nil
	}

	// get response
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

func internalFetchWithTimeout(ctx context.Context, client http.Client, url *url.URL) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, apiHeimdallTimeout)
	defer cancel()

	// request data once
	body, err := internalFetch(ctx, client, url)

	return body, err
}

// Close sends a signal to stop the running process
func (h *HeimdallClient) Close() {
	close(h.closeCh)
	h.client.CloseIdleConnections()
}
