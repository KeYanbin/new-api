package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
}

func TestApplyProtectToEffectivePricingAddsMarkup(t *testing.T) {
	protect := *ratio_setting.GetRatioProtectSetting()
	t.Cleanup(func() {
		config.UpdateConfigFromMap(config.GlobalConfig.Get("ratio_protect_setting"), map[string]string{
			"enabled":             fmtBool(protect.Enabled),
			"protect_model_ratio": fmtBool(protect.ProtectModelRatio),
			"markup_mode":         protect.MarkupMode,
			"markup_value":        fmtFloat(protect.MarkupValue),
			"skip_zero_upstream":  fmtBool(protect.SkipZeroUpstream),
		})
	})
	config.UpdateConfigFromMap(config.GlobalConfig.Get("ratio_protect_setting"), map[string]string{
		"enabled":             "true",
		"protect_model_ratio": "true",
		"markup_mode":         "add",
		"markup_value":        "0.1",
		"skip_zero_upstream":  "true",
	})
	protected := applyProtectToEffectivePricing(map[string]any{
		"model_ratio": map[string]any{"gpt": 0.2},
	})
	assert.Equal(t, 0.3, toFloat(valueMap(protected["model_ratio"])["gpt"]))
}

func fmtBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func fmtFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func toFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	default:
		return 0
	}
}

func TestParseUpstreamRatioBodyKeepsSiblingGroupRatio(t *testing.T) {
	body, err := common.Marshal(map[string]any{
		"success": true,
		"data": []map[string]any{
			{"model_name": "gpt-5", "model_ratio": 2.5, "quota_type": 0},
		},
		"group_ratio": map[string]any{"gptplus": 0.1, "gptproo": 0.2},
	})
	require.NoError(t, err)
	data, err := parseUpstreamRatioBody(body)
	require.NoError(t, err)
	assert.Equal(t, 2.5, toFloat(valueMap(data["model_ratio"])["gpt-5"]))
	assert.Equal(t, 0.1, toFloat(valueMap(data["group_ratio"])["gptplus"]))
	assert.Equal(t, 0.2, toFloat(valueMap(data["group_ratio"])["gptproo"]))
}

func TestMergeProtectUpstreamDataKeepsFirstSourceWins(t *testing.T) {
	merged := mergeProtectUpstreamData([]map[string]any{
		{
			"model_ratio": map[string]any{"gpt-5": 2.5},
			"group_ratio": map[string]any{"gptplus": 0.1},
		},
		{
			"model_ratio": map[string]any{"gpt-5": 9.0, "claude": 3.0},
			"group_ratio": map[string]any{"gptplus": 0.9, "gptproo": 0.2},
		},
	})
	assert.Equal(t, 2.5, toFloat(valueMap(merged["model_ratio"])["gpt-5"]))
	assert.Equal(t, 3.0, toFloat(valueMap(merged["model_ratio"])["claude"]))
	assert.Equal(t, 0.1, toFloat(valueMap(merged["group_ratio"])["gptplus"]))
	assert.Equal(t, 0.2, toFloat(valueMap(merged["group_ratio"])["gptproo"]))
}

func TestFetchOneUpstreamRatioAttachesChannelAuthorization(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})

	received := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
		encoded, err := common.Marshal(map[string]any{
			"success": true,
			"data":    []map[string]any{{"model_name": "gpt-5", "model_ratio": 2.5, "quota_type": 0}},
		})
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}))
	t.Cleanup(server.Close)

	baseURL := server.URL
	channel := &model.Channel{
		Name:    "protect-source",
		Type:    1,
		Key:     "sk-upstream-pricing",
		Status:  common.ChannelStatusEnabled,
		BaseURL: &baseURL,
		Models:  "gpt-5",
		Group:   "default",
	}
	require.NoError(t, db.Create(channel).Error)

	data, err := fetchOneUpstreamRatio(context.Background(), newUpstreamRatioHTTPClient(), dto.UpstreamDTO{
		ID:       channel.Id,
		Name:     channel.Name,
		BaseURL:  server.URL,
		Endpoint: "/api/pricing",
	}, 5)
	require.NoError(t, err)
	assert.Equal(t, "Bearer sk-upstream-pricing", <-received)
	assert.Equal(t, 2.5, toFloat(valueMap(data["model_ratio"])["gpt-5"]))

	_, err = fetchOneUpstreamRatio(context.Background(), newUpstreamRatioHTTPClient(), dto.UpstreamDTO{
		Name:     "anonymous",
		BaseURL:  server.URL,
		Endpoint: "/api/pricing",
	}, 5)
	require.NoError(t, err)
	assert.Empty(t, <-received)
}

func TestFetchOneUpstreamRatioWithAuthPrefersDedicatedToken(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})

	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
		encoded, err := common.Marshal(map[string]any{
			"success": true,
			"data":    []map[string]any{{"model_name": "gpt-5", "model_ratio": 2.5, "quota_type": 0}},
		})
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}))
	t.Cleanup(server.Close)

	baseURL := server.URL
	channel := &model.Channel{
		Name:    "protect-source",
		Type:    1,
		Key:     "sk-upstream-pricing",
		Status:  common.ChannelStatusEnabled,
		BaseURL: &baseURL,
		Models:  "gpt-5",
		Group:   "default",
	}
	require.NoError(t, db.Create(channel).Error)

	_, err = fetchOneUpstreamRatioWithAuth(context.Background(), newUpstreamRatioHTTPClient(), dto.UpstreamDTO{
		ID:       channel.Id,
		Name:     channel.Name,
		BaseURL:  server.URL,
		Endpoint: "/api/pricing",
	}, 5, "dashboard-jwt")
	require.NoError(t, err)
	assert.Equal(t, "Bearer dashboard-jwt", <-received)
}

func TestSub2APIPriceFetchAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name         string
		source       string
		token        string
		authStatus   int
		publicStatus int
		wantHeaders  []string
		wantError    string
	}{
		{name: "expired token falls back to public prices", source: ratio_setting.RatioProtectSourceSub2API, token: "expired-token", authStatus: 401, publicStatus: 200, wantHeaders: []string{"Bearer expired-token", ""}},
		{name: "valid token preserves account prices", source: ratio_setting.RatioProtectSourceSub2API, token: "valid-token", authStatus: 200, publicStatus: 200, wantHeaders: []string{"Bearer valid-token"}},
		{name: "public prices need no token", source: ratio_setting.RatioProtectSourceSub2API, publicStatus: 200, wantHeaders: []string{""}},
		{name: "private prices still require authentication", source: ratio_setting.RatioProtectSourceSub2API, token: "expired-token", authStatus: 401, publicStatus: 401, wantHeaders: []string{"Bearer expired-token", ""}, wantError: "401 Unauthorized"},
		{name: "anonymous rejection is not retried", source: ratio_setting.RatioProtectSourceSub2API, publicStatus: 401, wantHeaders: []string{""}, wantError: "401 Unauthorized"},
		{name: "forbidden is not retried", source: ratio_setting.RatioProtectSourceSub2API, token: "valid-token", authStatus: 403, publicStatus: 200, wantHeaders: []string{"Bearer valid-token"}, wantError: "403 Forbidden"},
		{name: "other sources retain authentication errors", source: ratio_setting.RatioProtectSourceNewAPI, token: "expired-token", authStatus: 401, publicStatus: 200, wantHeaders: []string{"Bearer expired-token"}, wantError: "401 Unauthorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := sub2APIPlazaBody(t)
			headers := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth := r.Header.Get("Authorization")
				headers <- auth
				status := tc.publicStatus
				if auth != "" {
					status = tc.authStatus
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write(body)
				}
			}))
			t.Cleanup(server.Close)
			result, err := fetchUpstreamBody(context.Background(), server.Client(), dto.UpstreamDTO{
				ID: 1, BaseURL: server.URL, Endpoint: "/custom/prices",
			}, 5, upstreamFetchOptions{Source: tc.source, AuthToken: tc.token, SkipChannelKey: true})
			if tc.wantError != "" {
				require.EqualError(t, err, tc.wantError)
				assert.Empty(t, result.Body)
			} else {
				require.NoError(t, err)
				assert.Equal(t, body, result.Body)
			}
			var received []string
			for len(headers) > 0 {
				received = append(received, <-headers)
			}
			assert.Equal(t, tc.wantHeaders, received)
		})
	}
}

func sub2APIPlazaBody(t *testing.T) []byte {
	t.Helper()
	body := map[string]any{
		"code":    0,
		"message": "success",
		"data": map[string]any{
			"groups": []any{
				map[string]any{
					"id": 9, "name": "GPT 稳定分组", "platform": "openai", "rate_multiplier": 0.07,
					"models": []any{
						map[string]any{
							"name": "gpt-5.5",
							"pricing": map[string]any{
								"billing_mode": "token", "input_price": 0.000005, "output_price": 0.000015,
								"cache_write_price": 0.00000625, "cache_read_price": 5e-7,
							},
						},
						map[string]any{
							"name": "codex-tiered",
							"pricing": map[string]any{
								"billing_mode": "token", "input_price": 0.000002, "output_price": 0.000009,
								"intervals": []any{
									map[string]any{"min_tokens": 0, "max_tokens": 272000, "input_price": 0.000002, "output_price": 0.000009},
									map[string]any{"min_tokens": 272000, "max_tokens": nil, "input_price": 0.000004, "output_price": 0.000018},
								},
							},
						},
						map[string]any{"name": "free-model", "pricing": map[string]any{"billing_mode": "token", "input_price": 0}},
						map[string]any{"name": "no-price"},
					},
				},
				map[string]any{
					"id": 49, "name": "CCMAX 勿蒸馏", "platform": "anthropic", "rate_multiplier": 0.8,
					"models": []any{
						map[string]any{"name": "gpt-5.5", "pricing": map[string]any{"billing_mode": "token", "input_price": 0.000005, "output_price": 0.000015}},
					},
				},
			},
		},
	}
	encoded, err := common.Marshal(body)
	require.NoError(t, err)
	return encoded
}

func TestConvertSub2APIPlazaToRatioDataAppliesGroupRate(t *testing.T) {
	data, notes, err := convertSub2APIPlazaToRatioData(bytes.NewReader(sub2APIPlazaBody(t)), "GPT 稳定分组")
	require.NoError(t, err)
	modelRatio := data["model_ratio"].(map[string]any)
	// 5e-6 USD/token * 0.07 = 3.5e-7 USD/token -> 0.35 USD per 1M -> ratio 0.175
	assert.Equal(t, 0.175, modelRatio["gpt-5.5"])
	assert.Equal(t, 0.0, modelRatio["free-model"])
	assert.NotContains(t, modelRatio, "no-price")
	assert.Equal(t, 3.0, data["completion_ratio"].(map[string]any)["gpt-5.5"])
	assert.Equal(t, 0.1, data["cache_ratio"].(map[string]any)["gpt-5.5"])
	assert.Equal(t, 1.25, data["create_cache_ratio"].(map[string]any)["gpt-5.5"])
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "阶梯计价")
	assert.Contains(t, notes[0], "codex-tiered")
}

func TestConvertSub2APIPlazaToRatioDataSelectsGroupByID(t *testing.T) {
	data, _, err := convertSub2APIPlazaToRatioData(bytes.NewReader(sub2APIPlazaBody(t)), "49")
	require.NoError(t, err)
	// group 49 charges 0.8x: 5e-6 * 0.8 = 4e-6 USD/token -> 4 USD per 1M -> ratio 2
	assert.Equal(t, 2.0, data["model_ratio"].(map[string]any)["gpt-5.5"])
}

func TestConvertSub2APIPlazaToRatioDataRejectsUnknownGroup(t *testing.T) {
	_, _, err := convertSub2APIPlazaToRatioData(bytes.NewReader(sub2APIPlazaBody(t)), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GPT 稳定分组")
}

func TestConvertSub2APIPlazaToRatioDataKeepsPerRequestPrice(t *testing.T) {
	body := []byte(`{"code":0,"message":"success","data":{"groups":[{"id":19,"name":"生图","rate_multiplier":0.2,"image_rate_independent":true,"image_rate_multiplier":0.5,"models":[{"name":"gpt-image","pricing":{"billing_mode":"image","per_request_price":0.4}}]}]}}`)
	data, _, err := convertSub2APIPlazaToRatioData(bytes.NewReader(body), "生图")
	require.NoError(t, err)
	assert.Equal(t, 0.2, data["model_price"].(map[string]any)["gpt-image"])
}

func TestParseSub2APIPlazaGroupsRejectsErrorPayload(t *testing.T) {
	_, err := parseSub2APIPlazaGroups([]byte(`{"code":401,"message":"UNAUTHORIZED"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNAUTHORIZED")
}

func sub2APIPlazaServer(t *testing.T, groupID int64, name string, rate float64, models map[string][2]float64) *httptest.Server {
	t.Helper()
	plazaModels := make([]map[string]any, 0, len(models))
	for modelName, price := range models {
		plazaModels = append(plazaModels, map[string]any{
			"name": modelName,
			"pricing": map[string]any{
				"billing_mode": "token",
				"input_price":  price[0],
				"output_price": price[1],
			},
		})
	}
	body := map[string]any{
		"code":    0,
		"message": "success",
		"data": map[string]any{
			"groups": []any{map[string]any{
				"id": groupID, "name": name, "platform": "openai", "rate_multiplier": rate, "models": plazaModels,
			}},
		},
	}
	encoded, err := common.Marshal(body)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ratio_setting.RatioProtectSub2APIDefaultEndpoint {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}))
	t.Cleanup(server.Close)
	return server
}

// ratioProtectMemoryDB boots an isolated in-memory database so the protect task
// can read channels, pricing options and the snapshot it writes back.
func ratioProtectMemoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMaster, previousSQLite := common.IsMasterNode, common.SQLitePath
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousOptions := common.OptionMap
	common.IsMasterNode = false
	common.SQLitePath = ":memory:"
	common.OptionMap = map[string]string{}
	t.Setenv("SQL_DSN", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitDB())
	db := model.DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Option{}, &model.Model{}, &model.Ability{}))
	t.Cleanup(func() {
		common.OptionMap = previousOptions
		common.IsMasterNode, common.SQLitePath = previousMaster, previousSQLite
		common.SetDatabaseTypes(previousMain, previousLog)
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	return db
}

func TestRunRatioProtectTaskFollowsEachChannelUpstreamPrice(t *testing.T) {
	db := ratioProtectMemoryDB(t)
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	restoreRatios := []struct {
		value   string
		restore func(string) error
	}{
		{ratio_setting.ModelRatio2JSONString(), ratio_setting.UpdateModelRatioByJSONString},
		{ratio_setting.CompletionRatio2JSONString(), ratio_setting.UpdateCompletionRatioByJSONString},
	}
	t.Cleanup(func() {
		for _, value := range restoreRatios {
			require.NoError(t, value.restore(value.value))
		}
		restore := map[string]string{}
		for key, value := range previousConfig {
			if after, ok := strings.CutPrefix(key, "ratio_protect_setting."); ok {
				restore[after] = value
			}
		}
		require.NoError(t, config.UpdateConfigFromMap(config.GlobalConfig.Get("ratio_protect_setting"), restore))
	})

	// Two upstreams with different group rates, one shared model.
	const (
		sharedModel = "gpt-5.5"
		onlyAModel  = "claude-haiku"
		onlyBModel  = "gemini-2.5"
	)
	serverA := sub2APIPlazaServer(t, 9, "GPT 稳定分组", 0.07, map[string][2]float64{
		sharedModel: {0.000005, 0.000015},
		onlyAModel:  {0.000001, 0.000005},
	})
	serverB := sub2APIPlazaServer(t, 49, "CCMAX 勿蒸馏", 0.8, map[string][2]float64{
		sharedModel: {0.000005, 0.000015},
		onlyBModel:  {0.000002, 0.000010},
	})
	channelA := &model.Channel{Name: "kedaya-A", Type: 1, Key: "sk-a", Status: common.ChannelStatusEnabled, BaseURL: lo.ToPtr(serverA.URL), Models: sharedModel, Group: "default"}
	channelB := &model.Channel{Name: "kedaya-B", Type: 1, Key: "sk-b", Status: common.ChannelStatusEnabled, BaseURL: lo.ToPtr(serverB.URL), Models: sharedModel, Group: "default"}
	require.NoError(t, db.Create(channelA).Error)
	require.NoError(t, db.Create(channelB).Error)

	// Local prices start at the upstream raw price, which the first run marks up.
	for name, ratio := range map[string]float64{sharedModel: 0.175, onlyAModel: 0.035, onlyBModel: 0.8} {
		snapshot, err := model.GetModelPricingSnapshot([]string{name})
		require.NoError(t, err)
		require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{{
			ModelName:       name,
			ExpectedVersion: snapshot.Entries[0].Version,
			Pricing:         model.PricingValues{"ModelRatio": ratio},
		}}))
	}

	sources, err := common.Marshal([]ratio_setting.RatioProtectChannelSource{
		{ChannelID: channelA.Id, Source: ratio_setting.RatioProtectSourceSub2API, UpstreamGroup: "GPT 稳定分组", MarkupMode: ratio_setting.RatioProtectModeAdd, MarkupValue: 0.1},
		{ChannelID: channelB.Id, Source: ratio_setting.RatioProtectSourceSub2API, UpstreamGroup: "CCMAX 勿蒸馏", MarkupMode: ratio_setting.RatioProtectModeAdd, MarkupValue: 0.1},
	})
	require.NoError(t, err)
	require.NoError(t, config.UpdateConfigFromMap(config.GlobalConfig.Get("ratio_protect_setting"), map[string]string{
		"enabled":           "true",
		"auto_apply":        "true",
		"notify":            "false",
		"markup_mode":       ratio_setting.RatioProtectModeAdd,
		"markup_value":      "0.1",
		"max_change_factor": "5",
		"sources":           string(sources),
		"source_states":     "{}",
	}))

	summary, err := runRatioProtectTaskOnce(context.Background(), false, nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{sharedModel, onlyAModel, onlyBModel}, summary.AppliedModels)
	require.Len(t, summary.Conflicts, 1)
	assert.Contains(t, summary.Conflicts[0], sharedModel)
	assert.Contains(t, summary.Conflicts[0], "kedaya-A")
	assert.Contains(t, summary.Conflicts[0], "kedaya-B")

	ratios := ratio_setting.GetModelRatioCopy()
	assert.InDelta(t, 0.275, ratios[sharedModel], 1e-9)
	assert.InDelta(t, 0.135, ratios[onlyAModel], 1e-9)
	assert.InDelta(t, 0.9, ratios[onlyBModel], 1e-9)

	var states map[string]ratio_setting.RatioProtectSourceState
	require.NoError(t, common.UnmarshalJsonStr(common.OptionMap["ratio_protect_setting.source_states"], &states))
	assert.Len(t, states, 2)
	for _, binding := range ratio_setting.GetRatioProtectSetting().EffectiveBindings() {
		state := states[binding.Fingerprint()]
		if binding.ChannelIDs[0] == channelA.Id {
			assert.Equal(t, map[string]float64{"model_ratio": 0.175}, state.LastSeen[sharedModel])
			assert.Equal(t, map[string]float64{"model_ratio": 0.035}, state.LastSeen[onlyAModel])
		} else {
			assert.Equal(t, map[string]float64{"model_ratio": 0.8}, state.LastSeen[onlyBModel])
		}
	}
}

func TestSub2APICustomEndpointUsesExplicitSource(t *testing.T) {
	body := sub2APIPlazaBody(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/custom/prices", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	upstream := dto.UpstreamDTO{BaseURL: server.URL, Endpoint: "/custom/prices"}
	groups, err := fetchUpstreamPriceGroups(context.Background(), upstream, "/custom/prices", "")
	require.NoError(t, err)
	require.NotEmpty(t, groups)
	data, err := fetchUpstreamPriceData(context.Background(), server.Client(), upstream, 5, upstreamFetchOptions{
		Source:         ratio_setting.RatioProtectSourceSub2API,
		UpstreamGroup:  "49",
		SkipChannelKey: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 2.0, data.Data["model_ratio"].(map[string]any)["gpt-5.5"])
}

func TestRatioProtectExplicitEmptySourcesSurviveReload(t *testing.T) {
	for _, raw := range []string{"[]", "null"} {
		t.Run(raw, func(t *testing.T) {
			setting := ratio_setting.RatioProtectSetting{}
			require.NoError(t, config.UpdateConfigFromMap(&setting, map[string]string{
				"channel_id": "7", "channel_ids": "[7,9]", "sources": raw,
			}))
			for range 2 {
				encoded, err := common.Marshal(setting)
				require.NoError(t, err)
				require.NoError(t, common.Unmarshal(encoded, &setting))
				if raw == "[]" {
					assert.Empty(t, setting.EffectiveBindings())
					assert.Empty(t, setting.SelectedChannelIDs())
				} else {
					require.Len(t, setting.EffectiveBindings(), 1)
					assert.Equal(t, []int{7, 9}, setting.SelectedChannelIDs())
				}
			}
		})
	}
}
