package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func protectSetting() *RatioProtectSetting {
	return &RatioProtectSetting{
		Enabled:           true,
		AutoApply:         true,
		MarkupMode:        RatioProtectModeAdd,
		MarkupValue:       0.1,
		ProtectModelRatio: true,
		SkipZeroUpstream:  true,
		MaxChangeFactor:   5,
	}
}

func TestApplyProtectMarkup(t *testing.T) {
	setting := protectSetting()
	assert.Equal(t, 0.3, ApplyProtectMarkup(0.2, setting))
	setting.MarkupMode = RatioProtectModeMultiply
	setting.MarkupValue = 1.1
	assert.Equal(t, 0.22, ApplyProtectMarkup(0.2, setting))
}

func TestApplyProtectToPricingSyncDataAddsMarkup(t *testing.T) {
	data := map[string]any{
		"model_ratio": map[string]any{"gpt": 0.2, "free": 0.0},
		"model_price": map[string]any{"paid": 1.0},
	}
	protected := ApplyProtectToPricingSyncDataWithSetting(data, protectSetting())
	assert.Equal(t, 0.3, protected["model_ratio"].(map[string]any)["gpt"])
	assert.Equal(t, 0.0, protected["model_ratio"].(map[string]any)["free"])
	assert.Equal(t, 1.0, protected["model_price"].(map[string]any)["paid"])
}

func TestApplyProtectToPricingSyncDataDisabledLeavesRawValues(t *testing.T) {
	data := map[string]any{"model_ratio": map[string]any{"gpt": 0.2}}
	setting := protectSetting()
	setting.Enabled = false
	protected := ApplyProtectToPricingSyncDataWithSetting(data, setting)
	assert.Equal(t, 0.2, protected["model_ratio"].(map[string]any)["gpt"])
}

func TestDecideRatioProtectFollowsUpstreamChange(t *testing.T) {
	setting := protectSetting()
	local := map[string]any{"model_ratio": map[string]any{"gpt": 0.3}}
	upstream := map[string]any{"model_ratio": map[string]any{"gpt": 0.3}}
	lastSeen := map[string]map[string]float64{"gpt": {"model_ratio": 0.2}}
	decisions := DecideRatioProtectActions(local, upstream, lastSeen, setting)
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionApply, decisions[0].Action)
	assert.Equal(t, 0.4, decisions[0].Target["model_ratio"])
}

func TestDecideRatioProtectKeepsCustomUntilUpstreamChanges(t *testing.T) {
	setting := protectSetting()
	local := map[string]any{"model_ratio": map[string]any{"gpt": 0.8}}
	upstream := map[string]any{"model_ratio": map[string]any{"gpt": 0.2}}
	lastSeen := map[string]map[string]float64{"gpt": {"model_ratio": 0.2}}
	decisions := DecideRatioProtectActions(local, upstream, lastSeen, setting)
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionUnchanged, decisions[0].Action)
}

func TestDecideRatioProtectFirstSyncAppliesMarkupToBareUpstream(t *testing.T) {
	setting := protectSetting()
	local := map[string]any{"model_ratio": map[string]any{"gpt": 0.2}}
	upstream := map[string]any{"model_ratio": map[string]any{"gpt": 0.2}}
	decisions := DecideRatioProtectActions(local, upstream, nil, setting)
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionApply, decisions[0].Action)
	assert.Equal(t, 0.3, decisions[0].Target["model_ratio"])
}

func TestDecideRatioProtectFirstSyncLeavesAlreadyProtectedPrice(t *testing.T) {
	setting := protectSetting()
	local := map[string]any{"model_ratio": map[string]any{"gpt": 0.3}}
	upstream := map[string]any{"model_ratio": map[string]any{"gpt": 0.2}}
	decisions := DecideRatioProtectActions(local, upstream, nil, setting)
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionUnchanged, decisions[0].Action)
}

func TestDecideRatioProtectSkipsNewAndExpressionModels(t *testing.T) {
	setting := protectSetting()
	local := map[string]any{
		"model_ratio":  map[string]any{"existing": 0.3},
		"billing_mode": map[string]any{"expr": "tiered_expr"},
		"billing_expr": map[string]any{"expr": `tier("base", p * 2)`},
	}
	upstream := map[string]any{
		"model_ratio":  map[string]any{"new": 0.2, "existing": 0.2},
		"billing_mode": map[string]any{"expr": "tiered_expr"},
		"billing_expr": map[string]any{"expr": `tier("base", p * 2)`},
	}
	decisions := DecideRatioProtectActions(local, upstream, nil, setting)
	actions := map[string]string{}
	for _, decision := range decisions {
		actions[decision.Name] = decision.Action
	}
	assert.Equal(t, RatioProtectActionSkipNew, actions["new"])
	assert.Equal(t, RatioProtectActionSkipExpression, actions["expr"])
	assert.Equal(t, RatioProtectActionUnchanged, actions["existing"])
}

func TestDecideRatioProtectSkipsAnomalousJumps(t *testing.T) {
	setting := protectSetting()
	local := map[string]any{"model_ratio": map[string]any{"gpt": 0.3}}
	upstream := map[string]any{"model_ratio": map[string]any{"gpt": 2}}
	lastSeen := map[string]map[string]float64{"gpt": {"model_ratio": 0.2}}
	decisions := DecideRatioProtectActions(local, upstream, lastSeen, setting)
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionSkipAnomaly, decisions[0].Action)
}
