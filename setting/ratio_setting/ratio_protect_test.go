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
	assert.Equal(t, 0.3, ApplyProtectMarkup(0.2, setting.Markup()))
	setting.MarkupMode = RatioProtectModeMultiply
	setting.MarkupValue = 1.1
	assert.Equal(t, 0.22, ApplyProtectMarkup(0.2, setting.Markup()))
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
	decisions := DecideRatioProtectActions(local, upstream, lastSeen, setting, setting.Markup())
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionApply, decisions[0].Action)
	assert.Equal(t, 0.4, decisions[0].Target["model_ratio"])
}

func TestDecideRatioProtectKeepsCustomUntilUpstreamChanges(t *testing.T) {
	setting := protectSetting()
	local := map[string]any{"model_ratio": map[string]any{"gpt": 0.8}}
	upstream := map[string]any{"model_ratio": map[string]any{"gpt": 0.2}}
	lastSeen := map[string]map[string]float64{"gpt": {"model_ratio": 0.2}}
	decisions := DecideRatioProtectActions(local, upstream, lastSeen, setting, setting.Markup())
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionUnchanged, decisions[0].Action)
}

func TestDecideRatioProtectFirstSyncAppliesMarkupToBareUpstream(t *testing.T) {
	setting := protectSetting()
	local := map[string]any{"model_ratio": map[string]any{"gpt": 0.2}}
	upstream := map[string]any{"model_ratio": map[string]any{"gpt": 0.2}}
	decisions := DecideRatioProtectActions(local, upstream, nil, setting, setting.Markup())
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionApply, decisions[0].Action)
	assert.Equal(t, 0.3, decisions[0].Target["model_ratio"])
}

func TestDecideRatioProtectFirstSyncLeavesAlreadyProtectedPrice(t *testing.T) {
	setting := protectSetting()
	local := map[string]any{"model_ratio": map[string]any{"gpt": 0.3}}
	upstream := map[string]any{"model_ratio": map[string]any{"gpt": 0.2}}
	decisions := DecideRatioProtectActions(local, upstream, nil, setting, setting.Markup())
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
	decisions := DecideRatioProtectActions(local, upstream, nil, setting, setting.Markup())
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
	decisions := DecideRatioProtectActions(local, upstream, lastSeen, setting, setting.Markup())
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionSkipAnomaly, decisions[0].Action)
}

func TestSelectedChannelIDsFallsBackToChannelID(t *testing.T) {
	setting := &RatioProtectSetting{ChannelID: 7}
	assert.Equal(t, []int{7}, setting.SelectedChannelIDs())
	setting.ChannelIDs = []int{3, 3, 1, 0, -100}
	assert.Equal(t, []int{3, 1, -100}, setting.SelectedChannelIDs())
	assert.Equal(t, "-100,1,3|/api/pricing", RatioProtectSourceKeys(setting.ChannelIDs, "/api/pricing"))
	assert.Equal(t, "-100,1,3|/api/pricing|gpt plus,gpt pro", RatioProtectSourceKeysWithGroups(setting.ChannelIDs, "/api/pricing", []string{"gpt pro", " gpt plus ", "gpt plus", ""}))
}

func TestFilterPricingSyncDataByGroupsKeepsSelectedModelsAndGroupRatios(t *testing.T) {
	data := map[string]any{
		"model_ratio": map[string]any{"gpt-plus": 0.2, "gpt-pro": 0.3, "other": 0.4},
		"model_price": map[string]any{"gpt-plus": 1.0, "other": 2.0},
		"group_ratio": map[string]any{"gpt plus": 0.2, "gpt pro": 0.3, "special": 0.18},
	}
	filtered := FilterPricingSyncDataByGroups(data, []string{"gpt plus"}, func(name string) []string {
		switch name {
		case "gpt-plus":
			return []string{"gpt plus"}
		case "gpt-pro":
			return []string{"gpt pro"}
		case "other":
			return []string{"special"}
		default:
			return nil
		}
	})
	assert.Equal(t, 0.2, filtered["model_ratio"].(map[string]any)["gpt-plus"])
	assert.NotContains(t, filtered["model_ratio"].(map[string]any), "gpt-pro")
	assert.NotContains(t, filtered["model_ratio"].(map[string]any), "other")
	assert.Equal(t, 0.2, filtered["group_ratio"].(map[string]any)["gpt plus"])
	assert.NotContains(t, filtered["group_ratio"].(map[string]any), "gpt pro")
	assert.NotContains(t, filtered["group_ratio"].(map[string]any), "special")
}

func TestApplyProtectToPricingSyncDataAddsGroupRatioMarkup(t *testing.T) {
	data := map[string]any{
		"group_ratio": map[string]any{"gptplus": 0.1, "free": 0.0},
	}
	setting := protectSetting()
	setting.ProtectGroupRatio = true
	protected := ApplyProtectToPricingSyncDataWithSetting(data, setting)
	assert.Equal(t, 0.2, protected["group_ratio"].(map[string]any)["gptplus"])
	assert.Equal(t, 0.0, protected["group_ratio"].(map[string]any)["free"])
}

func TestDecideGroupRatioProtectFollowsUpstreamChange(t *testing.T) {
	setting := protectSetting()
	setting.ProtectGroupRatio = true
	local := map[string]any{"group_ratio": map[string]any{"gptplus": 0.2}}
	upstream := map[string]any{"group_ratio": map[string]any{"gptplus": 0.2}}
	lastSeen := map[string]float64{"gptplus": 0.1}
	decisions := DecideGroupRatioProtectActions(local, upstream, lastSeen, setting, setting.Markup())
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionApply, decisions[0].Action)
	assert.Equal(t, 0.3, decisions[0].Target[RatioProtectFieldGroupRatio])
}

func TestDecideGroupRatioProtectKeepsCustomUntilUpstreamChanges(t *testing.T) {
	setting := protectSetting()
	setting.ProtectGroupRatio = true
	local := map[string]any{"group_ratio": map[string]any{"gptplus": 0.8}}
	upstream := map[string]any{"group_ratio": map[string]any{"gptplus": 0.1}}
	lastSeen := map[string]float64{"gptplus": 0.1}
	decisions := DecideGroupRatioProtectActions(local, upstream, lastSeen, setting, setting.Markup())
	require.Len(t, decisions, 1)
	assert.Equal(t, RatioProtectActionUnchanged, decisions[0].Action)
}

func TestEffectiveBindingsBindsOneChannelToOneUpstreamPrice(t *testing.T) {
	setting := protectSetting()
	setting.Endpoint = "/api/pricing"
	setting.Sources = []RatioProtectChannelSource{
		{ChannelID: 7, Source: RatioProtectSourceSub2API, UpstreamGroup: "GPT 稳定分组", MarkupMode: RatioProtectModeMultiply, MarkupValue: 1.2},
		{ChannelID: 9, Source: RatioProtectSourceSub2API, UpstreamGroup: "CCMAX 勿蒸馏", MarkupMode: RatioProtectModeAdd, MarkupValue: 0.05},
	}
	bindings := setting.EffectiveBindings()
	require.Len(t, bindings, 2)
	assert.Equal(t, []int{7}, bindings[0].ChannelIDs)
	assert.Equal(t, "GPT 稳定分组", bindings[0].UpstreamGroup)
	assert.Equal(t, RatioProtectMarkup{Mode: RatioProtectModeMultiply, Value: 1.2}, bindings[0].Markup)
	assert.Equal(t, RatioProtectMarkup{Mode: RatioProtectModeAdd, Value: 0.05}, bindings[1].Markup)
	assert.Equal(t, []int{7, 9}, setting.SelectedChannelIDs())
}

func TestEffectiveBindingsKeepsLegacyMergedBinding(t *testing.T) {
	setting := protectSetting()
	setting.ChannelIDs = []int{3, 5}
	setting.Endpoint = "/api/pricing"
	bindings := setting.EffectiveBindings()
	require.Len(t, bindings, 1)
	assert.Equal(t, []int{3, 5}, bindings[0].ChannelIDs)
	assert.Equal(t, "3,5|/api/pricing", bindings[0].Key)
	assert.Equal(t, RatioProtectSourceAuto, bindings[0].Source)
}

func TestNormalizeRatioProtectSourcesDropsDuplicateAndInheritsMarkup(t *testing.T) {
	setting := protectSetting()
	setting.Sources = []RatioProtectChannelSource{
		{ChannelID: 4, Source: "unknown", SyncGroups: []string{" pro ", "pro", ""}},
		{ChannelID: 4, Source: RatioProtectSourceNewAPI},
		{ChannelID: 0},
	}
	setting.SyncGroups = []string{"default"}
	normalizeRatioProtectSetting(setting)
	require.Len(t, setting.Sources, 1)
	assert.Equal(t, RatioProtectSourceNewAPI, setting.Sources[0].Source)
	assert.Equal(t, []string{"pro"}, setting.Sources[0].SyncGroups)
	assert.Equal(t, RatioProtectModeAdd, setting.Sources[0].MarkupMode)
	assert.Equal(t, setting.MarkupValue, setting.Sources[0].MarkupValue)
	assert.Equal(t, []int{4}, setting.ChannelIDs)
}

func TestLoadBindingStateResetsWhenBindingChanges(t *testing.T) {
	setting := protectSetting()
	setting.Sources = []RatioProtectChannelSource{{ChannelID: 7, Source: RatioProtectSourceSub2API, UpstreamGroup: "GPT 稳定分组"}}
	binding := setting.EffectiveBindings()[0]
	setting.SourceStates = map[string]RatioProtectSourceState{
		binding.Fingerprint(): {Binding: binding.Fingerprint(), LastSeen: map[string]map[string]float64{"gpt": {"model_ratio": 0.2}}},
	}
	state := setting.LoadBindingState(binding)
	assert.Equal(t, 0.2, state.LastSeen["gpt"]["model_ratio"])

	setting.Sources[0].UpstreamGroup = "GPT 不降智"
	assert.Empty(t, setting.LoadBindingState(setting.EffectiveBindings()[0]).LastSeen)
}

func TestLoadBindingStateReadsLegacySnapshot(t *testing.T) {
	setting := protectSetting()
	setting.ChannelID = 3
	setting.Endpoint = "/api/pricing"
	setting.LastSeenSource = "3|/api/pricing"
	setting.LastSeen = map[string]map[string]float64{"gpt": {"model_ratio": 0.2}}
	state := setting.LoadBindingState(setting.EffectiveBindings()[0])
	assert.Equal(t, 0.2, state.LastSeen["gpt"]["model_ratio"])
}

func TestMergeRatioProtectSourceDecisionsKeepsFirstBindingAndReportsConflict(t *testing.T) {
	first := RatioProtectSourceDecisions{
		Key:  "ch:7",
		Name: "channel 7 (GPT 稳定分组)",
		Decisions: []RatioProtectDecision{
			{Name: "gpt-5.6", Action: RatioProtectActionApply, Raw: map[string]float64{"model_ratio": 0.5}, Target: map[string]float64{"model_ratio": 0.6}},
		},
	}
	second := RatioProtectSourceDecisions{
		Key:  "ch:9",
		Name: "channel 9 (CCMAX)",
		Decisions: []RatioProtectDecision{
			{Name: "gpt-5.6", Action: RatioProtectActionApply, Raw: map[string]float64{"model_ratio": 0.8}, Target: map[string]float64{"model_ratio": 0.9}},
			{Name: "claude-haiku", Action: RatioProtectActionApply, Target: map[string]float64{"model_ratio": 0.1}},
		},
	}
	merged, winners, conflicts := MergeRatioProtectSourceDecisions([]RatioProtectSourceDecisions{first, second})
	require.Len(t, merged, 2)
	assert.Equal(t, map[string]string{"gpt-5.6": first.Key, "claude-haiku": second.Key}, winners)
	require.Len(t, conflicts, 1)
	assert.Equal(t, "gpt-5.6", conflicts[0].Model)
	assert.Equal(t, first.Name, conflicts[0].Winner)
	assert.Equal(t, second.Name, conflicts[0].Loser)
	assert.Equal(t, []string{"model_ratio"}, conflicts[0].Changed)
}

func TestMergeRatioProtectSourceDecisionsAcceptsAgreement(t *testing.T) {
	decision := RatioProtectDecision{Name: "gpt-5.6", Action: RatioProtectActionApply, Target: map[string]float64{"model_ratio": 0.6}}
	merged, _, conflicts := MergeRatioProtectSourceDecisions([]RatioProtectSourceDecisions{
		{Name: "a", Decisions: []RatioProtectDecision{decision}},
		{Name: "b", Decisions: []RatioProtectDecision{decision}},
	})
	require.Len(t, merged, 1)
	assert.Empty(t, conflicts)
}
