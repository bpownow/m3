// Copyright (c) 2018 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package native

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/m3db/m3/src/cmd/services/m3query/config"
	"github.com/m3db/m3/src/query/api/v1/handler"
	"github.com/m3db/m3/src/query/api/v1/handler/prometheus"
	"github.com/m3db/m3/src/query/block"
	"github.com/m3db/m3/src/query/executor"
	"github.com/m3db/m3/src/query/models"
	"github.com/m3db/m3/src/query/storage"
	"github.com/m3db/m3/src/query/storage/mock"
	"github.com/m3db/m3/src/query/test"
	"github.com/m3db/m3/src/x/instrument"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromReadHandler_Read(t *testing.T) {
	testPromReadHandler_Read(t, block.NewResultMetadata(), "")
	testPromReadHandler_Read(t, buildWarningMeta("foo", "bar"), "foo_bar")
	testPromReadHandler_Read(t, block.ResultMetadata{Exhaustive: false},
		handler.LimitHeaderSeriesLimitApplied)
}

func testPromReadHandler_Read(
	t *testing.T,
	resultMeta block.ResultMetadata,
	ex string,
) {
	values, bounds := test.GenerateValuesAndBounds(nil, nil)

	setup := newTestSetup()
	promRead := setup.Handlers.Read

	seriesMeta := test.NewSeriesMeta("dummy", len(values))
	m := block.Metadata{
		Bounds:         bounds,
		Tags:           models.NewTags(0, models.NewTagOptions()),
		ResultMetadata: resultMeta,
	}

	b := test.NewBlockFromValuesWithMetaAndSeriesMeta(m, seriesMeta, values)
	setup.Storage.SetFetchBlocksResult(block.Result{Blocks: []block.Block{b}}, nil)

	req, _ := http.NewRequest("GET", PromReadURL, nil)
	req.URL.RawQuery = defaultParams().Encode()

	r, parseErr := testParseParams(req)
	require.Nil(t, parseErr)
	assert.Equal(t, models.FormatPromQL, r.FormatType)
	result, err := read(context.TODO(), promRead.engine,
		setup.QueryOpts, setup.FetchOpts, promRead.tagOpts, httptest.NewRecorder(),
		r, instrument.NewOptions())

	seriesList := result.series
	require.NoError(t, err)
	require.Len(t, seriesList, 2)
	s := seriesList[0]

	assert.Equal(t, 5, s.Values().Len())
	for i := 0; i < s.Values().Len(); i++ {
		assert.Equal(t, float64(i), s.Values().ValueAt(i))
	}
}

type M3QLResp []struct {
	Target     string            `json:"target"`
	Tags       map[string]string `json:"tags"`
	Datapoints [][]float64       `json:"datapoints"`
	StepSizeMs int               `json:"step_size_ms"`
}

func TestPromReadHandlerRead(t *testing.T) {
	testPromReadHandlerRead(t, block.NewResultMetadata(), "")
	testPromReadHandlerRead(t, buildWarningMeta("foo", "bar"), "foo_bar")
	testPromReadHandlerRead(t, block.ResultMetadata{Exhaustive: false},
		handler.LimitHeaderSeriesLimitApplied)
}

func testPromReadHandlerRead(
	t *testing.T,
	resultMeta block.ResultMetadata,
	ex string,
) {
	values, bounds := test.GenerateValuesAndBounds(nil, nil)

	setup := newTestSetup()
	promRead := setup.Handlers.Read

	seriesMeta := test.NewSeriesMeta("dummy", len(values))
	meta := block.Metadata{
		Bounds:         bounds,
		Tags:           models.NewTags(0, models.NewTagOptions()),
		ResultMetadata: resultMeta,
	}

	b := test.NewBlockFromValuesWithMetaAndSeriesMeta(meta, seriesMeta, values)
	setup.Storage.SetFetchBlocksResult(block.Result{Blocks: []block.Block{b}}, nil)

	req, _ := http.NewRequest("GET", PromReadURL, nil)
	req.Header.Add("X-M3-Render-Format", "m3ql")
	req.URL.RawQuery = defaultParams().Encode()

	recorder := httptest.NewRecorder()
	promRead.ServeHTTP(recorder, req)

	header := recorder.Header().Get(handler.LimitHeader)
	assert.Equal(t, ex, header)

	var m3qlResp M3QLResp
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &m3qlResp))

	assert.Len(t, m3qlResp, 2)
	assert.Equal(t, "dummy0", m3qlResp[0].Target)
	assert.Equal(t, map[string]string{"__name__": "dummy0", "dummy0": "dummy0"}, m3qlResp[0].Tags)
	assert.Equal(t, 10000, m3qlResp[0].StepSizeMs)
	assert.Equal(t, "dummy1", m3qlResp[1].Target)
	assert.Equal(t, map[string]string{"__name__": "dummy1", "dummy1": "dummy1"}, m3qlResp[1].Tags)
	assert.Equal(t, 10000, m3qlResp[1].StepSizeMs)

}

func newReadRequest(t *testing.T, params url.Values) *http.Request {
	req, err := http.NewRequest("GET", PromReadURL, nil)
	require.NoError(t, err)
	req.URL.RawQuery = params.Encode()
	return req
}

type testSetup struct {
	Storage     mock.Storage
	Handlers    testSetupHandlers
	QueryOpts   *executor.QueryOptions
	FetchOpts   *storage.FetchOptions
	TimeoutOpts *prometheus.TimeoutOpts
}

type testSetupHandlers struct {
	Read        *PromReadHandler
	InstantRead *PromReadInstantHandler
}

func newTestSetup() *testSetup {
	mockStorage := mock.NewMockStorage()

	instrumentOpts := instrument.NewOptions()
	engineOpts := executor.NewEngineOptions().
		SetStore(mockStorage).
		SetLookbackDuration(time.Minute).
		SetGlobalEnforcer(nil).
		SetInstrumentOptions(instrumentOpts)
	engine := executor.NewEngine(engineOpts)
	fetchOptsBuilderCfg := handler.FetchOptionsBuilderOptions{}
	fetchOptsBuilder := handler.NewFetchOptionsBuilder(fetchOptsBuilderCfg)
	tagOpts := models.NewTagOptions()
	limitsConfig := &config.LimitsConfiguration{}
	keepNans := false

	read := NewPromReadHandler(engine, fetchOptsBuilder, tagOpts,
		limitsConfig, timeoutOpts, keepNans, instrumentOpts)

	instantRead := NewPromReadInstantHandler(engine, fetchOptsBuilder,
		tagOpts, timeoutOpts, instrumentOpts)

	return &testSetup{
		Storage: mockStorage,
		Handlers: testSetupHandlers{
			Read:        read,
			InstantRead: instantRead,
		},
		QueryOpts:   &executor.QueryOptions{},
		FetchOpts:   storage.NewFetchOptions(),
		TimeoutOpts: timeoutOpts,
	}
}

func TestPromReadHandler_ServeHTTP_maxComputedDatapoints(t *testing.T) {
	setup := newTestSetup()
	setup.Handlers.Read.limitsCfg = &config.LimitsConfiguration{
		PerQuery: config.PerQueryLimitsConfiguration{
			PrivateMaxComputedDatapoints: 3599,
		},
	}

	params := defaultParams()
	params.Set(startParam, time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano))
	params.Set(endParam, time.Date(2018, 1, 1, 1, 0, 0, 0, time.UTC).Format(time.RFC3339Nano))
	params.Set(handler.StepParam, (time.Second).String())
	req := newReadRequest(t, params)

	recorder := httptest.NewRecorder()
	setup.Handlers.Read.ServeHTTP(recorder, req)
	resp := recorder.Result()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	d, err := ioutil.ReadAll(resp.Body)
	require.NoError(t, err)

	// not a public struct in xhttp, but it's small.
	var errResp struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(d, &errResp))

	expected := "querying from 2018-01-01 00:00:00 +0000 UTC to 2018-01-01 01:00:00 +0000 UTC with step size 1s " +
		"would result in too many datapoints (end - start / step > 3599). Either decrease the query resolution " +
		"(?step=XX), decrease the time window, or increase the limit (`limits.maxComputedDatapoints`)"
	assert.Equal(t, expected, errResp.Error)
}

func TestPromReadHandler_validateRequest(t *testing.T) {
	dt := func(year int, month time.Month, day, hour int) time.Time {
		return time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
	}

	cases := []struct {
		Name          string
		Params        *models.RequestParams
		Max           int64
		ErrorExpected bool
	}{{
		Name: "under limit",
		Params: &models.RequestParams{
			Step:  time.Second,
			Start: dt(2018, 1, 1, 0),
			End:   dt(2018, 1, 1, 1),
		},
		Max:           3601,
		ErrorExpected: false,
	}, {
		Name: "at limit",
		Params: &models.RequestParams{
			Step:  time.Second,
			Start: dt(2018, 1, 1, 0),
			End:   dt(2018, 1, 1, 1),
		},
		Max:           3600,
		ErrorExpected: false,
	}, {
		Name: "over limit",
		Params: &models.RequestParams{
			Step:  time.Second,
			Start: dt(2018, 1, 1, 0),
			End:   dt(2018, 1, 1, 1),
		},
		Max:           3599,
		ErrorExpected: true,
	}, {
		Name: "large query, limit disabled (0)",
		Params: &models.RequestParams{
			Step:  time.Second,
			Start: dt(2018, 1, 1, 0),
			End:   dt(2018, 1, 1, 1),
		},
		Max:           0,
		ErrorExpected: false,
	}, {
		Name: "large query, limit disabled (negative)",
		Params: &models.RequestParams{
			Step:  time.Second,
			Start: dt(2018, 1, 1, 0),
			End:   dt(2018, 1, 1, 1),
		},
		Max:           -50,
		ErrorExpected: false,
	}, {
		Name: "uneven step over limit",
		Params: &models.RequestParams{
			Step:  34 * time.Minute,
			Start: dt(2018, 1, 1, 0),
			End:   dt(2018, 1, 1, 11),
		},
		Max:           1,
		ErrorExpected: true,
	}, {
		Name: "uneven step under limit",
		Params: &models.RequestParams{
			Step:  34 * time.Minute,
			Start: dt(2018, 1, 1, 0),
			End:   dt(2018, 1, 1, 1),
		},
		Max:           2,
		ErrorExpected: false},
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			setup := newTestSetup()
			setup.Handlers.Read.limitsCfg = &config.LimitsConfiguration{
				PerQuery: config.PerQueryLimitsConfiguration{
					PrivateMaxComputedDatapoints: tc.Max,
				},
			}

			err := setup.Handlers.Read.validateRequest(tc.Params)

			if tc.ErrorExpected {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
