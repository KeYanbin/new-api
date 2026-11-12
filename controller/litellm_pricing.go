package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/shopspring/decimal"
)

// GitHub's API serves the same repository file when the raw hostname is
// unreachable. Keep part of the caller's timeout available for that fallback.
func fetchModelPriceRepoPricing(ctx context.Context, client *http.Client) (map[string]any, error) {
	urls := []string{
		modelPriceRepoPresetBaseURL + modelPriceRepoPresetPath,
		"https://api.github.com/repos/Wei-Shaw/model-price-repo/contents/model_prices_and_context_window.json?ref=main",
	}
	var failures []error
	for index, source := range urls {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(append(failures, err)...)
		}
		attemptCtx := ctx
		cancel := func() {}
		if index == 0 {
			timeout := 3 * time.Second
			if deadline, ok := ctx.Deadline(); ok {
				timeout = min(timeout, time.Until(deadline)/2)
			}
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		request, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, source, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		request.Header.Set("User-Agent", "new-api-pricing-sync")
		request.Header.Set("Accept", "application/vnd.github.raw+json")
		response, err := client.Do(request)
		var body []byte
		if err == nil {
			if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("%s: %s", source, response.Status)
			} else {
				body, err = io.ReadAll(io.LimitReader(response.Body, maxRatioConfigBytes+1))
				if len(body) > maxRatioConfigBytes {
					err = fmt.Errorf("pricing response exceeds size limit")
				}
			}
			response.Body.Close()
		}
		cancel()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if index > 0 {
			// Some API proxies return the Contents JSON despite the raw Accept header.
			var content struct {
				Encoding string `json:"encoding"`
				Content  string `json:"content"`
			}
			if common.Unmarshal(body, &content) == nil && content.Encoding == "base64" {
				body, err = base64.StdEncoding.DecodeString(content.Content)
				if err != nil {
					failures = append(failures, err)
					continue
				}
			}
		}
		data, err := convertLiteLLMToPricingData(bytes.NewReader(body))
		if err == nil {
			return data, nil
		}
		failures = append(failures, err)
	}
	return nil, errors.Join(failures...)
}

// LiteLLM stores USD per token. Expressions preserve independent cache prices
// and whole-request context tiers without deriving legacy ratios from input.
func convertLiteLLMToPricingData(reader io.Reader) (map[string]any, error) {
	var catalog map[string]common.RawMessage
	if err := common.DecodeJson(reader, &catalog); err != nil {
		return nil, fmt.Errorf("failed to decode LiteLLM pricing: %w", err)
	}
	modes := make(map[string]any)
	expressions := make(map[string]any)
	for name, raw := range catalog {
		if strings.TrimSpace(name) == "" {
			continue
		}
		var fields map[string]common.RawMessage
		if err := common.Unmarshal(raw, &fields); err != nil {
			continue
		}
		expression, ok := buildLiteLLMPricingExpression(fields)
		if !ok {
			continue
		}
		modes[name] = billing_setting.BillingModeTieredExpr
		expressions[name] = expression
	}
	if len(expressions) == 0 {
		return nil, fmt.Errorf("no valid LiteLLM token pricing entries found")
	}
	return map[string]any{
		billing_setting.BillingModeField: modes,
		billing_setting.BillingExprField: expressions,
	}, nil
}

func buildLiteLLMPricingExpression(fields map[string]common.RawMessage) (string, bool) {
	var mode string
	if raw, exists := fields["mode"]; exists {
		if err := common.Unmarshal(raw, &mode); err != nil || (mode != "" && mode != "chat") {
			return "", false
		}
	}
	dimensions := []struct {
		field    string
		variable string
	}{
		{"input_cost_per_token", "p"},
		{"output_cost_per_token", "c"},
		{"cache_read_input_token_cost", "cr"},
		{"cache_creation_input_token_cost", "cc"},
		{"cache_creation_input_token_cost_above_1hr", "cc1h"},
		{"cache_creation_input_token_cost_above_12hr", "cc12h"},
		{"input_cost_per_audio_token", "ai"},
		{"output_cost_per_audio_token", "ao"},
		{"input_cost_per_image_token", "img"},
		{"output_cost_per_image_token", "img_o"},
		{"cache_read_input_image_token_cost", "img_cr"},
	}
	base := make(map[string]float64)
	tiers := make(map[int]map[string]float64)
	for _, dimension := range dimensions {
		for field, raw := range fields {
			threshold := 0
			if field != dimension.field {
				suffix, ok := strings.CutPrefix(field, dimension.field+"_above_")
				if !ok {
					continue
				}
				// Context tiers on TTL-specific cache prices belong to cc1h or
				// cc12h; their TTL must not be parsed as a context threshold.
				if dimension.variable == "cc" && (strings.HasPrefix(suffix, "1hr_") || strings.HasPrefix(suffix, "12hr_")) {
					continue
				}
				number, ok := strings.CutSuffix(suffix, "k_tokens")
				if !ok {
					// TTL prices have their own dimensions; service-tier prices
					// are not inferred from request fields the channel may filter.
					continue
				}
				count, err := strconv.Atoi(number)
				if err != nil || count <= 0 || count > 2147483 {
					return "", false
				}
				threshold = count * 1000
			}
			var cost *float64
			if err := common.Unmarshal(raw, &cost); err != nil {
				return "", false
			}
			if cost == nil {
				continue
			}
			// Shift decimal units without exposing binary multiplication noise
			// in the administrator's pricing expression (for example, $0.1).
			price := decimal.NewFromFloat(*cost).Shift(6).InexactFloat64()
			if !isValidNonNegativeCost(price) {
				return "", false
			}
			if threshold == 0 {
				base[dimension.variable] = price
				continue
			}
			if tiers[threshold] == nil {
				tiers[threshold] = make(map[string]float64)
			}
			tiers[threshold][dimension.variable] = price
		}
	}
	// Missing prices must never become an implicit free input or output rate.
	if _, exists := base["p"]; !exists {
		return "", false
	}
	if _, exists := base["c"]; !exists {
		return "", false
	}
	// These extension fields use a separate provider-specific multiplier contract.
	// Reject them rather than importing only the cheaper base tier.
	if _, exists := fields["long_context_input_token_threshold"]; exists {
		return "", false
	}
	thresholds := make([]int, 0, len(tiers))
	for threshold, prices := range tiers {
		for variable := range prices {
			if _, exists := base[variable]; !exists {
				return "", false
			}
		}
		thresholds = append(thresholds, threshold)
	}
	slices.Sort(thresholds)
	var expression strings.Builder
	for index := range len(thresholds) + 1 {
		label := "base"
		if index > 0 {
			threshold := thresholds[index-1]
			for variable, price := range tiers[threshold] {
				base[variable] = price
			}
			label = "above_" + strconv.Itoa(threshold)
		}
		if index < len(thresholds) {
			fmt.Fprintf(&expression, "len <= %d ? ", thresholds[index])
		}
		fmt.Fprintf(&expression, "tier(%q, ", label)
		separator := ""
		for _, dimension := range dimensions {
			price, exists := base[dimension.variable]
			if !exists {
				continue
			}
			expression.WriteString(separator)
			expression.WriteString(dimension.variable)
			expression.WriteString(" * ")
			expression.WriteString(strconv.FormatFloat(price, 'f', -1, 64))
			separator = " + "
		}
		expression.WriteByte(')')
		if index < len(thresholds) {
			expression.WriteString(" : ")
		}
	}
	return expression.String(), true
}
