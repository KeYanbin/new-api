package openai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type responsesFailedWriter struct {
	gin.ResponseWriter
	failed chan struct{}
	once   sync.Once
	writes int
}

func (w *responsesFailedWriter) WriteString(string) (int, error) {
	w.writes++
	w.once.Do(func() { close(w.failed) })
	return 0, io.ErrClosedPipe
}

func TestResponsesDisconnectRecoversFinalUsage(t *testing.T) {
	oldTimeout, oldDrain := constant.StreamingTimeout, constant.ResponsesUsageDrainTimeout
	constant.StreamingTimeout, constant.ResponsesUsageDrainTimeout = 30, 60
	t.Cleanup(func() { constant.StreamingTimeout, constant.ResponsesUsageDrainTimeout = oldTimeout, oldDrain })
	for _, tc := range []struct {
		name         string
		first        string
		terminal     string
		beforeFirst  bool
		writeFailure bool
	}{
		{name: "before first event", beforeFirst: true, terminal: "response.completed"},
		{name: "tool arguments before final usage", first: `{"type":"response.function_call_arguments.delta","delta":"args"}`, terminal: "response.completed"},
		{name: "text before final usage", first: `{"type":"response.output_text.delta","delta":"hello"}`, terminal: "response.completed"},
		{name: "failed client write without cancelled context", first: `{"type":"response.created"}`, writeFailure: true, terminal: "response.completed"},
		{name: "incomplete response with billed usage", first: `{"type":"response.created"}`, terminal: "response.incomplete"},
		{name: "failed response with billed usage", first: `{"type":"response.created"}`, terminal: "response.failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pr, pw := io.Pipe()
			defer pr.Close()
			defer pw.Close()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			c.Set(common.RequestIdKey, "responses-disconnect-regression")
			info := &relaycommon.RelayInfo{IsStream: true, DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"}}
			var failedWriter *responsesFailedWriter
			if tc.writeFailure {
				failedWriter = &responsesFailedWriter{ResponseWriter: c.Writer, failed: make(chan struct{})}
				c.Writer = failedWriter
			} else if tc.beforeFirst {
				cancel()
			} else {
				c.Writer = &cancelAfterWriter{ResponseWriter: c.Writer, needle: "data:", cancel: cancel}
			}
			var usage *dto.Usage
			var apiErr *types.NewAPIError
			done := make(chan struct{})
			go func() {
				defer close(done)
				usage, apiErr = OaiResponsesStreamHandler(c, info, &http.Response{Body: pr})
			}()
			if tc.first != "" {
				_, err := io.WriteString(pw, "data: "+tc.first+"\n\n")
				require.NoError(t, err)
			}
			disconnected := ctx.Done()
			if failedWriter != nil {
				disconnected = failedWriter.failed
			}
			select {
			case <-disconnected:
			case <-time.After(5 * time.Second):
				t.Fatal("client did not disconnect")
			}
			terminal := `{"type":"` + tc.terminal + `","response":{"usage":{"input_tokens":1100,"output_tokens":70,"total_tokens":1170,"input_tokens_details":{"cached_tokens":1000},"output_tokens_details":{"reasoning_tokens":60}}}}`
			_, err := io.WriteString(pw, "data: "+terminal+"\n\n")
			require.NoError(t, err)
			// Keep the upstream open: final usage must finish accounting without waiting for EOF.
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("final usage did not finish the stream")
			}
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, 1100, usage.PromptTokens)
			assert.Equal(t, 70, usage.CompletionTokens)
			assert.Equal(t, 1170, usage.TotalTokens)
			assert.Equal(t, 1000, usage.PromptTokensDetails.CachedTokens)
			assert.Equal(t, 60, usage.CompletionTokenDetails.ReasoningTokens)
			assert.True(t, info.StreamStatus.ClientDisconnected)
			assert.Equal(t, "recovered", info.StreamStatus.UsageRecovery)
			assert.NotContains(t, w.Body.String(), tc.terminal)
			if tc.beforeFirst {
				assert.Empty(t, w.Body.String())
			}
			if failedWriter != nil {
				assert.Equal(t, 1, failedWriter.writes)
			}
			_, err = io.WriteString(pw, "data: "+terminal+"\n\n")
			assert.ErrorIs(t, err, io.ErrClosedPipe, "terminal usage closes the stream before any duplicate terminal can be counted")
			other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 1000, 0.1, 0, 1).Snapshot()
			require.Contains(t, other, "admin_info")
			assert.Equal(t, "recovered", other["admin_info"].(map[string]any)["stream_usage_recovery"])
		})
	}
}

func TestResponsesDisconnectWithoutFinalUsageRequiresReconciliation(t *testing.T) {
	oldTimeout, oldDrain := constant.StreamingTimeout, constant.ResponsesUsageDrainTimeout
	t.Cleanup(func() { constant.StreamingTimeout, constant.ResponsesUsageDrainTimeout = oldTimeout, oldDrain })
	for _, tc := range []struct {
		name    string
		drain   int
		endBody bool
	}{
		{name: "upstream EOF without usage", drain: 60, endBody: true},
		{name: "hard drain deadline closes upstream", drain: 1},
		{name: "draining disabled closes upstream immediately", drain: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			constant.StreamingTimeout, constant.ResponsesUsageDrainTimeout = 30, tc.drain
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			pr, pw := io.Pipe()
			defer pr.Close()
			defer pw.Close()
			if tc.endBody {
				require.NoError(t, pw.Close())
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			info := &relaycommon.RelayInfo{IsStream: true, DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"}}
			var usage *dto.Usage
			var apiErr *types.NewAPIError
			done := make(chan struct{})
			go func() {
				defer close(done)
				usage, apiErr = OaiResponsesStreamHandler(c, info, &http.Response{Body: pr})
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream did not close within the drain deadline")
			}
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Zero(t, usage.TotalTokens, "missing usage must not be invented")
			assert.Equal(t, "pending_reconciliation", info.StreamStatus.UsageRecovery)
			assert.Empty(t, w.Body.String())
			if !tc.endBody {
				_, err := io.WriteString(pw, "data: late\n\n")
				assert.ErrorIs(t, err, io.ErrClosedPipe)
			}
			if tc.drain == 1 {
				assert.True(t, info.StreamStatus.HasErrors())
			}
			other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 0.1, 0, 1).Snapshot()
			require.Contains(t, other, "admin_info")
			assert.Equal(t, "pending_reconciliation", other["admin_info"].(map[string]any)["stream_usage_recovery"])
		})
	}
}

func TestOaiResponsesHandlerCountsOutputCallsNotDeclarations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	operation_setting.SetToolPriceForTest("priced_fn", 5.0)
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest("priced_fn")
	})

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Tools: []map[string]any{
			{"type": "web_search_preview"},
			{"type": "file_search"},
		},
		Output: []dto.ResponsesOutput{
			{Type: dto.BuildInCallWebSearchCall},
			{Type: dto.BuildInCallWebSearchCall},
			{Type: dto.BuildInCallFunctionCall, Name: "priced_fn"},
			{Type: dto.BuildInCallFunctionCall, Name: "unpriced_fn"},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearchPreview: {ToolName: dto.BuildInToolWebSearchPreview, CallCount: 0},
				dto.BuildInToolFileSearch:       {ToolName: dto.BuildInToolFileSearch, CallCount: 0},
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	usage, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 2, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview].CallCount)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolFileSearch].CallCount)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, "priced_fn")
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools["priced_fn"].CallCount)
	assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, "unpriced_fn")
}

func TestOaiResponsesHandlerDeclaredToolsWithoutOutputCountZero(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Tools: []map[string]any{
			{"type": "web_search_preview"},
			{"type": "file_search"},
		},
		Output: []dto.ResponsesOutput{
			{Type: "message", Role: "assistant"},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolWebSearchPreview: {ToolName: dto.BuildInToolWebSearchPreview, CallCount: 0},
				dto.BuildInToolFileSearch:       {ToolName: dto.BuildInToolFileSearch, CallCount: 0},
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview].CallCount)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolFileSearch].CallCount)
}

func TestOaiResponsesHandlerCountsCompletedImageGenerationOutputs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Status: []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_1",
				Status: "completed",
				Result: "base64-a",
			},
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_2",
				Status: "completed",
				Result: "base64-b",
			},
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_empty",
				Status: "completed",
				Result: "",
			},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-5.1"}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolImageGeneration)
	assert.Equal(t, 2, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
	assert.False(t, c.GetBool("image_generation_call"))
}

func TestOaiResponsesHandlerCountsImageGenerationOnIncompleteStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, err := common.Marshal(dto.OpenAIResponsesResponse{
		Status: []byte(`"incomplete"`),
		Output: []dto.ResponsesOutput{
			{
				Type:   dto.ResponsesOutputTypeImageGenerationCall,
				ID:     "img_1",
				Status: "completed",
				Result: "base64-a",
			},
		},
		Usage: &dto.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &relaycommon.ResponsesUsageInfo{
			BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				dto.BuildInToolImageGeneration: {ToolName: dto.BuildInToolImageGeneration, CallCount: 0},
			},
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func runResponsesImageBillingStream(t *testing.T, events ...string) *relaycommon.RelayInfo {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: ")
		body.WriteString(event)
		body.WriteString("\n\n")
	}
	body.WriteString("data: [DONE]\n\n")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "responses-image-billing-test")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1",
		DisablePing:     true,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-5.1",
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body.String())),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, info.ResponsesUsageInfo)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolImageGeneration)
	return info
}

func TestOaiResponsesStreamHandlerDeduplicatesCompletedImageOutput(t *testing.T) {
	item := `{"type":"image_generation_call","id":"img_1","call_id":"call_1","status":"completed","result":"base64-a"}`
	info := runResponsesImageBillingStream(
		t,
		`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
		`{"type":"response.completed","response":{"status":"completed","output":[`+item+`],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)

	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestOaiResponsesStreamHandlerCountsImageOutputOnIncomplete(t *testing.T) {
	info := runResponsesImageBillingStream(
		t,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"image_generation_call","id":"img_1","status":"completed","result":"base64-a"}}`,
		`{"type":"response.incomplete","response":{"status":"incomplete"}}`,
	)

	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestOaiResponsesStreamHandlerDoesNotCountPartialImageEvent(t *testing.T) {
	info := runResponsesImageBillingStream(
		t,
		`{"type":"response.image_generation_call.partial_image","output_index":0,"partial_image_b64":"partial-bytes"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[]}}`,
	)

	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestOaiResponsesHandlerRewritesSGLangCreatedAtToInt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeSGLang},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","object":"response","created_at":1786588600.0,"status":"completed","output":[]}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	_, apiErr := OaiResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Contains(t, w.Body.String(), `"created_at":1786588600`)
	assert.NotContains(t, w.Body.String(), `1786588600.0`)
}

func TestOaiResponsesStreamHandlerRewritesSGLangCreatedAtToInt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "sglang-created-at-test")

	info := &relaycommon.RelayInfo{
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeSGLang,
			UpstreamModelName: "served-model",
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1.7865886E9,\"status\":\"completed\",\"output\":[]}}\n\n" +
				"data: [DONE]\n\n",
		)),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Contains(t, w.Body.String(), `"created_at":1786588600`)
	assert.NotContains(t, w.Body.String(), `1.7865886E9`)
	assert.NotContains(t, w.Body.String(), `1786588600.0`)
}

func TestOaiResponsesStreamHandlerKeepsNonSGLangCreatedAt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "openai-created-at-test")

	info := &relaycommon.RelayInfo{
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: "gpt-test"},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1786588600.0,\"status\":\"completed\",\"output\":[]}}\n\n" +
				"data: [DONE]\n\n",
		)),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Contains(t, w.Body.String(), `1786588600.0`)
}

// A converted hosted web search reaches Azure and Xinference Chat Completions
// as web_search_options, like OpenAI; the upstream decides whether it applies.
// Azure Responses bills web search by tool_usage.web_search.num_requests (Bing
// transactions) under bing_web_search, and the explicit zeros silence the
// web_search_call items, which leaves one surcharge row.
func TestAzureHostedWebSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	t.Run("Claude to Chat", func(t *testing.T) {
		const body = `{"model":"gpt-5.1","max_tokens":64,"messages":[{"role":"user","content":"Weather?"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`
		for _, channelType := range []int{constant.ChannelTypeAzure, constant.ChannelTypeXinference, constant.ChannelTypeOpenAI} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{
				RelayFormat: types.RelayFormatClaude,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelType: channelType, UpstreamModelName: "gpt-5.1"},
			}
			adaptor := &Adaptor{}
			adaptor.Init(info)
			var request dto.ClaudeRequest
			require.NoError(t, common.UnmarshalJsonStr(body, &request))
			converted, err := adaptor.ConvertClaudeRequest(c, info, &request)
			require.NoError(t, err)
			wire, err := common.Marshal(converted)
			require.NoError(t, err)
			assert.True(t, strings.Contains(string(wire), `"web_search_options"`), channelType)
			var codes []string
			for _, diagnostic := range info.ConversionDiagnostics() {
				codes = append(codes, diagnostic.Code)
			}
			assert.False(t, slices.Contains(codes, "hosted_web_search_omitted"), channelType)
		}
	})

	const searchCall = `{"type":"web_search_call","status":"completed","action":{"type":"search","query":"go"}}`
	const usage = `"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13},"tool_usage":{"web_search":{"num_requests":5}}`
	output := strings.Repeat(searchCall+",", 3) + `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Go 1.25"}]}`
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{
			name:        "json",
			contentType: "application/json",
			body:        `{"id":"resp_1","object":"response","model":"gpt-5.1","status":"completed","output":[` + output + `],` + usage + `}`,
		},
		{
			name:        "stream",
			contentType: "text/event-stream",
			body: strings.Repeat(`data: {"type":"response.output_item.done","output_index":0,"item":`+searchCall+"}\n\n", 3) +
				`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.1","status":"completed","output":[` + output + `],` + usage + "}}\n\ndata: [DONE]\n\n",
		},
	}
	for _, tt := range tests {
		t.Run("Responses "+tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{
				RelayMode:       relayconstant.RelayModeResponses,
				RelayFormat:     types.RelayFormatOpenAIResponses,
				OriginModelName: "gpt-5.1",
				IsStream:        tt.name == "stream",
				DisablePing:     true,
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeAzure, UpstreamModelName: "gpt-5.1"},
			}
			adaptor := &Adaptor{}
			adaptor.Init(info)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
				Header:     http.Header{"Content-Type": []string{tt.contentType}},
			}
			var apiErr *types.NewAPIError
			if info.IsStream {
				_, apiErr = OaiResponsesStreamHandler(c, info, resp)
			} else {
				_, apiErr = OaiResponsesHandler(c, info, resp)
			}
			require.Nil(t, apiErr)
			billed := map[string]int{}
			for name, tool := range info.ResponsesUsageInfo.BuiltInTools {
				if tool.CallCount > 0 {
					billed[name] = tool.CallCount
				}
			}
			assert.Equal(t, map[string]int{"bing_web_search": 5}, billed)
		})
	}
}
