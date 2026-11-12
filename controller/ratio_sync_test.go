package controller

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pricingHTTPTransport func(*http.Request) (*http.Response, error)

func (transport pricingHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestModelPriceRepoFallsBackToOfficialAPI(t *testing.T) {
	pricing := `{"m":{"input_cost_per_token":0.000002,"output_cost_per_token":0.000008}}`
	wrapped, err := common.Marshal(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(pricing))})
	require.NoError(t, err)
	cases := []struct {
		name      string
		rawStatus int
		rawBody   string
		rawErr    error
		apiBody   string
		apiErr    error
		wantHosts []string
		wantErr   bool
	}{
		{"raw address succeeds without contacting API", 200, pricing, nil, "", nil, []string{"raw.githubusercontent.com"}, false},
		{"connection reset falls back to raw API response", 0, "", errors.New("connection reset"), pricing, nil, []string{"raw.githubusercontent.com", "api.github.com"}, false},
		{"failed raw status falls back to Contents encoding", 503, "", nil, string(wrapped), nil, []string{"raw.githubusercontent.com", "api.github.com"}, false},
		{"invalid raw pricing falls back to API", 200, "{}", nil, pricing, nil, []string{"raw.githubusercontent.com", "api.github.com"}, false},
		{"raw timeout preserves API request budget", 0, "", context.DeadlineExceeded, pricing, nil, []string{"raw.githubusercontent.com", "api.github.com"}, false},
		{"both addresses fail without importing prices", 0, "", errors.New("connection reset"), "", errors.New("API unavailable"), []string{"raw.githubusercontent.com", "api.github.com"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var hosts []string
			client := &http.Client{Transport: pricingHTTPTransport(func(request *http.Request) (*http.Response, error) {
				hosts = append(hosts, request.URL.Hostname())
				assert.Empty(t, request.Header.Get("Authorization"))
				assert.Equal(t, "application/vnd.github.raw+json", request.Header.Get("Accept"))
				status, body, err := tc.rawStatus, tc.rawBody, tc.rawErr
				if request.URL.Hostname() == "api.github.com" {
					assert.Equal(t, "main", request.URL.Query().Get("ref"))
					assert.Equal(t, "/repos/Wei-Shaw/model-price-repo/contents/model_prices_and_context_window.json", request.URL.Path)
					status, body, err = 200, tc.apiBody, tc.apiErr
				} else {
					parentDeadline, _ := ctx.Deadline()
					rawDeadline, ok := request.Context().Deadline()
					assert.True(t, ok && rawDeadline.Before(parentDeadline), "the API fallback retains part of the caller's timeout")
				}
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			data, err := fetchModelPriceRepoPricing(ctx, client)
			assert.Equal(t, tc.wantHosts, hosts)
			if tc.wantErr {
				assert.Error(t, err)
				assert.Nil(t, data)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, map[string]any{"m": `tier("base", p * 2 + c * 8)`}, valueMap(data["billing_expr"]))
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &http.Client{Transport: pricingHTTPTransport(func(*http.Request) (*http.Response, error) {
		t.Error("canceled fetch must not contact either address")
		return nil, context.Canceled
	})}
	_, err = fetchModelPriceRepoPricing(ctx, client)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestLiteLLMPricingPreservesTokenRatesAndContextTiers(t *testing.T) {
	contextPricing := `{"input_cost_per_token":0.0000025,"output_cost_per_token":0.000015,"cache_read_input_token_cost":0.00000025,"input_cost_per_token_above_272k_tokens":0.000005,"output_cost_per_token_above_272k_tokens":0.0000225,"cache_read_input_token_cost_above_272k_tokens":0.0000005,"input_cost_per_token_priority":0.000005}`
	contextExpression := `len <= 272000 ? tier("base", p * 2.5 + c * 15 + cr * 0.25) : tier("above_272000", p * 5 + c * 22.5 + cr * 0.5)`
	gptPricing := `{"mode":"chat","input_cost_per_token":0.000002,"output_cost_per_token":0.00001,"cache_read_input_token_cost":0.0000001,"cache_creation_input_token_cost":0.0000025,"cache_creation_input_token_cost_above_12hr":0.000005,"input_cost_per_token_above_272k_tokens":0.000004,"output_cost_per_token_above_272k_tokens":0.000015,"cache_read_input_token_cost_above_272k_tokens":0.0000002,"cache_creation_input_token_cost_above_272k_tokens":0.000005,"cache_creation_input_token_cost_above_12hr_above_272k_tokens":0.00001}`
	gptExpression := `len <= 272000 ? tier("base", p * 2 + c * 10 + cr * 0.1 + cc * 2.5 + cc12h * 5) : tier("above_272000", p * 4 + c * 15 + cr * 0.2 + cc * 5 + cc12h * 10)`
	cases := []struct {
		name       string
		pricing    string
		expression string
		params     billingexpr.TokenParams
		cost       float64
	}{
		{
			name:       "Claude alias keeps both cache TTL prices and explicit free cache reads",
			pricing:    `{"mode":"chat","input_cost_per_token":0.000005,"output_cost_per_token":0.000025,"cache_read_input_token_cost":0,"cache_creation_input_token_cost":0.00000625,"cache_creation_input_token_cost_above_1hr":0.00001}`,
			expression: `tier("base", p * 5 + c * 25 + cr * 0 + cc * 6.25 + cc1h * 10)`,
			params:     billingexpr.TokenParams{P: 100, C: 10, CR: 200, CC: 20, CC1h: 30},
			cost:       1175,
		},
		{
			name:       "zero input price retains paid output",
			pricing:    `{"input_cost_per_token":0,"output_cost_per_token":0.000001}`,
			expression: `tier("base", p * 0 + c * 1)`,
			params:     billingexpr.TokenParams{P: 100, C: 10},
			cost:       10,
		},
		{
			name:       "GPT 6.1 sol keeps 12 hour cache prices at the context boundary",
			pricing:    gptPricing,
			expression: gptExpression,
			params:     billingexpr.TokenParams{P: 1000, C: 100, CR: 100, CC: 20, CC12h: 30, Len: 272000},
			cost:       3210,
		},
		{
			name:       "GPT 6.1 sol increases 12 hour cache prices above the context boundary",
			pricing:    gptPricing,
			expression: gptExpression,
			params:     billingexpr.TokenParams{P: 1000, C: 100, CR: 100, CC: 20, CC12h: 30, Len: 272001},
			cost:       5920,
		},
		{
			name:       "Claude increases one hour cache prices independently in long contexts",
			pricing:    `{"input_cost_per_token":0.000005,"output_cost_per_token":0.000025,"cache_creation_input_token_cost":0.00000625,"cache_creation_input_token_cost_above_1hr":0.00001,"cache_creation_input_token_cost_above_200k_tokens":0.0000125,"cache_creation_input_token_cost_above_1hr_above_200k_tokens":0.00002}`,
			expression: `len <= 200000 ? tier("base", p * 5 + c * 25 + cc * 6.25 + cc1h * 10) : tier("above_200000", p * 5 + c * 25 + cc * 12.5 + cc1h * 20)`,
			params:     billingexpr.TokenParams{P: 100, C: 10, CC: 20, CC1h: 30, Len: 200001},
			cost:       1600,
		},
		{
			name:       "exact context threshold retains base prices",
			pricing:    contextPricing,
			expression: contextExpression,
			params:     billingexpr.TokenParams{P: 1000, C: 100, CR: 100, Len: 272000},
			cost:       4025,
		},
		{
			name:       "context threshold uses total input including cached tokens",
			pricing:    contextPricing,
			expression: contextExpression,
			params:     billingexpr.TokenParams{P: 1000, C: 100, CR: 100, Len: 272001},
			cost:       7300,
		},
		{
			name:       "multiple tiers inherit unchanged output prices",
			pricing:    `{"input_cost_per_token":0.000005,"output_cost_per_token":0.000025,"input_cost_per_token_above_200k_tokens":0.000015,"input_cost_per_token_above_100k_tokens":0.00001}`,
			expression: `len <= 100000 ? tier("base", p * 5 + c * 25) : len <= 200000 ? tier("above_100000", p * 10 + c * 25) : tier("above_200000", p * 15 + c * 25)`,
			params:     billingexpr.TokenParams{P: 100, C: 10, Len: 200000},
			cost:       1250,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := convertLiteLLMToPricingData(strings.NewReader(`{"claude-alias-thinking":` + tc.pricing + `}`))
			require.NoError(t, err)
			assert.Equal(t, billing_setting.BillingModeTieredExpr, valueMap(data["billing_mode"])["claude-alias-thinking"])
			expression, ok := valueMap(data["billing_expr"])["claude-alias-thinking"].(string)
			require.True(t, ok)
			assert.Equal(t, tc.expression, expression)
			cost, _, err := billingexpr.RunExpr(expression, tc.params)
			require.NoError(t, err)
			assert.Equal(t, tc.cost, cost)
			assert.NotContains(t, data, "model_ratio")
		})
	}
}

func TestLiteLLMPricingRejectsIncompleteAndInvalidPrices(t *testing.T) {
	cases := []string{
		`{"input_cost_per_token":0}`,
		`{"input_cost_per_token":null,"output_cost_per_token":0}`,
		`{"input_cost_per_token":-1,"output_cost_per_token":0}`,
		`{"input_cost_per_token":1e308,"output_cost_per_token":0}`,
		`{"input_cost_per_token":"invalid","output_cost_per_token":0}`,
		`{"input_cost_per_token":0,"output_cost_per_token":0,"cache_read_input_token_cost":-1}`,
		`{"input_cost_per_token":0,"output_cost_per_token":0,"input_cost_per_token_above_200k_tokens":-1}`,
		`{"input_cost_per_token":0,"output_cost_per_token":0,"cache_read_input_token_cost_above_200k_tokens":1}`,
		`{"input_cost_per_token":0,"output_cost_per_token":0,"cache_creation_input_token_cost":0,"cache_creation_input_token_cost_above_12hr_above_272k_tokens":1}`,
		`{"input_cost_per_token":0,"output_cost_per_token":0,"cache_creation_input_token_cost_above_12hr":0,"cache_creation_input_token_cost_above_12hr_above_272k_tokens":-1}`,
		`{"input_cost_per_token":0,"output_cost_per_token":0,"cache_creation_input_token_cost_above_1hr":0,"cache_creation_input_token_cost_above_1hr_above_invalidk_tokens":1}`,
		`{"input_cost_per_token":0,"output_cost_per_token":0,"long_context_input_token_threshold":32000}`,
		`{"mode":"image_generation","input_cost_per_token":0,"output_cost_per_token":0}`,
	}
	for _, pricing := range cases {
		t.Run(pricing, func(t *testing.T) {
			data, err := convertLiteLLMToPricingData(strings.NewReader(`{"invalid":` + pricing + `,"free":{"input_cost_per_token":0,"output_cost_per_token":0},"sample_spec":"metadata"}`))
			require.NoError(t, err)
			assert.Equal(t, map[string]any{"free": `tier("base", p * 0 + c * 0)`}, valueMap(data["billing_expr"]))
		})
	}
	for _, body := range []string{`{}`, `{"model":{}}`, `{"model":`} {
		_, err := convertLiteLLMToPricingData(strings.NewReader(body))
		assert.Error(t, err)
	}
}

func TestPricingSyncFetchesModelPriceRepoPreset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, modelPriceRepoPresetPath, r.URL.Path)
		assert.Empty(t, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"sync-repo-alias":{"input_cost_per_token":0.000005,"output_cost_per_token":0.000025,"cache_creation_input_token_cost_above_1hr":0.00001}}`))
	}))
	defer server.Close()
	var response struct {
		Success bool
		Data    struct {
			Prices      map[string]struct{ Upstreams map[string]map[string]any }
			TestResults []dto.TestResult `json:"test_results"`
		}
	}
	body := map[string]any{"upstreams": []map[string]any{
		{"id": modelPriceRepoPresetID, "name": modelPriceRepoPresetName, "base_url": server.URL},
	}}
	recorder := modelManagementRequest(t, FetchUpstreamRatios, http.MethodPost, "/api/ratio_sync/fetch", body, &response)
	require.True(t, response.Success, recorder.Body.String())
	require.Len(t, response.Data.TestResults, 1)
	assert.Equal(t, "success", response.Data.TestResults[0].Status)
	assert.Equal(t, map[string]any{
		"billing_mode": "tiered_expr",
		"billing_expr": `tier("base", p * 5 + c * 25 + cc1h * 10)`,
	}, response.Data.Prices["sync-repo-alias"].Upstreams["model-price-repo(-102)"])
}

func TestPricingSyncExpressionPriority(t *testing.T) {
	expression := `tier("base", p * 2 + c * 8 + cr * 0)`
	cases := []struct {
		name       string
		local      map[string]any
		source     map[string]any
		wantFields []string
	}{
		{"equal expressions suppress stale ratios", map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}}, map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}, "model_ratio": map[string]float64{"m": 3}, "model_price": map[string]float64{"m": 2}}, nil},
		{"local expression excludes legacy-only source", map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}}, map[string]any{"model_ratio": map[string]float64{"m": 3}, "completion_ratio": map[string]float64{"m": 2}}, nil},
		{"expression imports without legacy conflicts", map[string]any{"model_ratio": map[string]float64{"m": 1}}, map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": expression}, "model_ratio": map[string]float64{"m": 3}, "model_price": map[string]float64{"m": 2}}, []string{"billing_mode", "billing_expr"}},
		{"inactive expression follows explicit ratio mode", map[string]any{"model_ratio": map[string]float64{"m": 1}}, map[string]any{"billing_mode": map[string]string{"m": "ratio"}, "billing_expr": map[string]string{"m": expression}, "model_ratio": map[string]float64{"m": 3}}, []string{"model_ratio"}},
		{"empty active expression never imports a false free price", map[string]any{}, map[string]any{"billing_mode": map[string]string{"m": "tiered_expr"}, "billing_expr": map[string]string{"m": " "}, "model_ratio": map[string]float64{"m": 0}}, nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			diff := buildDifferences(tt.local, []struct {
				name string
				data map[string]any
			}{{"source", tt.source}})
			fields := make([]string, 0, len(diff["m"]))
			for field := range diff["m"] {
				fields = append(fields, field)
			}
			assert.ElementsMatch(t, tt.wantFields, fields)
		})
	}
}

func TestRatioConfigExportsEffectiveExpressions(t *testing.T) {
	before := config.GlobalConfig.ExportAllConfigs()
	expose := ratio_setting.IsExposeRatioEnabled()
	t.Cleanup(func() {
		config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": before["billing_setting.billing_mode"], "billing_expr": before["billing_setting.billing_expr"]})
		ratio_setting.SetExposeRatioEnabled(expose)
	})
	config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": `{"sync-export":"tiered_expr"}`, "billing_expr": `{"sync-export":"tier(\"base\", p * 2)"}`})
	ratio_setting.SetExposeRatioEnabled(true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ratio_config", nil)
	GetRatioConfig(c)
	var response struct {
		Success bool
		Data    map[string]any
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, billing_setting.BillingModeTieredExpr, valueMap(response.Data["billing_mode"])["sync-export"])
	assert.Equal(t, `tier("base", p * 2)`, valueMap(response.Data["billing_expr"])["sync-export"])
}

func TestPricingSyncCompleteSourcesAndArrayFormats(t *testing.T) {
	before := config.GlobalConfig.ExportAllConfigs()
	oldRatios, oldCompletion := ratio_setting.ModelRatio2JSONString(), ratio_setting.CompletionRatio2JSONString()
	t.Cleanup(func() {
		config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": before["billing_setting.billing_mode"], "billing_expr": before["billing_setting.billing_expr"]})
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(oldCompletion))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"sync-token":1}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"sync-token":2}`))
	expression := `len <= 200000 ? tier("short", p * 2 + c * 8 + cr * 0) : tier("long", p * 4 + c * 12)`
	expressions, err := common.Marshal(map[string]string{"sync-already": expression})
	require.NoError(t, err)
	config.UpdateConfigFromMap(config.GlobalConfig.Get("billing_setting"), map[string]string{"billing_mode": `{"sync-already":"tiered_expr"}`, "billing_expr": string(expressions)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		if r.URL.Path == "/ratio_config" {
			data = map[string]any{
				"billing_mode":     map[string]string{"sync-already": "tiered_expr", "sync-expression": "tiered_expr"},
				"billing_expr":     map[string]string{"sync-already": expression, "sync-expression": expression},
				"model_ratio":      map[string]float64{"sync-already": 9, "sync-expression": 9, "sync-token": 1},
				"model_price":      map[string]float64{"sync-expression": 4},
				"completion_ratio": map[string]float64{"sync-token": 4},
				"cache_ratio":      map[string]float64{"sync-token": 0},
			}
		} else {
			data = []map[string]any{
				{"model_name": "sync-already", "model_ratio": 5, "completion_ratio": 3},
				{"model_name": "sync-expression", "model_ratio": 2, "model_price": 1},
				{"model_name": "sync-array-expression", "billing_mode": "tiered_expr", "billing_expr": expression, "quota_type": 1, "model_price": 0},
				{"model_name": "sync-unpriced"},
				{"model_name": "sync-invalid-expression", "billing_mode": "tiered_expr", "billing_expr": "", "model_ratio": 0},
				{"model_name": "sync-free", "model_ratio": 0, "completion_ratio": 0},
			}
		}
		encoded, err := common.Marshal(map[string]any{"success": true, "data": data})
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}))
	defer server.Close()
	var response struct {
		Success bool
		Data    struct {
			Differences map[string]map[string]dto.DifferenceItem
			Prices      map[string]struct {
				Current   map[string]any
				Upstreams map[string]map[string]any
			}
			TestResults []dto.TestResult `json:"test_results"`
		}
	}
	body := map[string]any{"upstreams": []map[string]any{
		{"id": 1, "name": "Expressions", "base_url": server.URL, "endpoint": "/ratio_config"},
		{"id": 2, "name": "Legacy", "base_url": server.URL, "endpoint": "/pricing"},
	}}
	recorder := modelManagementRequest(t, FetchUpstreamRatios, http.MethodPost, "/api/channel/fetch_upstream_ratios", body, &response)
	require.True(t, response.Success, recorder.Body.String())
	require.Len(t, response.Data.TestResults, 2)
	for _, result := range response.Data.TestResults {
		require.Equal(t, "success", result.Status, result.Error)
	}
	assert.NotContains(t, response.Data.Differences, "sync-already")
	assert.NotContains(t, response.Data.Differences, "sync-unpriced")
	assert.NotContains(t, response.Data.Differences, "sync-invalid-expression")
	assert.Equal(t, map[string]any{"billing_mode": "tiered_expr", "billing_expr": expression}, response.Data.Prices["sync-expression"].Upstreams["Expressions(1)"])
	assert.NotContains(t, response.Data.Prices["sync-expression"].Upstreams, "Legacy(2)")
	assert.Equal(t, map[string]any{"billing_mode": "tiered_expr", "billing_expr": expression}, response.Data.Prices["sync-array-expression"].Upstreams["Legacy(2)"])
	assert.Equal(t, float64(1), response.Data.Prices["sync-token"].Upstreams["Expressions(1)"]["model_ratio"], "unchanged base prices are included for a complete price preview")
	assert.Equal(t, float64(4), response.Data.Prices["sync-token"].Upstreams["Expressions(1)"]["completion_ratio"])
	assert.Equal(t, float64(0), response.Data.Prices["sync-token"].Upstreams["Expressions(1)"]["cache_ratio"])
	assert.Equal(t, float64(0), response.Data.Prices["sync-free"].Upstreams["Legacy(2)"]["model_ratio"])
	assert.NotContains(t, response.Data.Prices, "sync-already")
	body["include_unchanged"] = true
	recorder = modelManagementRequest(t, FetchUpstreamRatios, http.MethodPost, "/api/ratio_sync/fetch", body, &response)
	require.True(t, response.Success, recorder.Body.String())
	assert.Equal(t, map[string]any{"billing_mode": "tiered_expr", "billing_expr": expression}, response.Data.Prices["sync-already"].Upstreams["Expressions(1)"])
	assert.NotContains(t, response.Data.Prices["sync-already"].Upstreams, "Legacy(2)")
	assert.NotContains(t, response.Data.Prices, "sync-unpriced")
	assert.NotContains(t, response.Data.Prices, "sync-invalid-expression")
	assert.NotContains(t, response.Data.Differences, "sync-already")
}
