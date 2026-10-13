package ratio_setting

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

const (
	RatioProtectModeAdd      = "add"
	RatioProtectModeMultiply = "multiply"

	RatioProtectFieldModelRatio = "model_ratio"
	RatioProtectFieldModelPrice = "model_price"
	RatioProtectFieldGroupRatio = "group_ratio"

	DefaultRatioProtectIntervalMinutes = 15
	MinRatioProtectIntervalMinutes     = 5
	MaxRatioProtectIntervalMinutes     = 1440
	DefaultRatioProtectMarkupValue     = 0.1
	DefaultRatioProtectMaxChangeFactor = 5.0
)

const (
	RatioProtectSourceAuto    = ""
	RatioProtectSourceNewAPI  = "newapi"
	RatioProtectSourceSub2API = "sub2api"

	RatioProtectDefaultEndpoint        = "/api/pricing"
	RatioProtectSub2APIDefaultEndpoint = "/api/v1/model-plaza"
)

const (
	RatioProtectActionApply            = "apply"
	RatioProtectActionUnchanged        = "unchanged"
	RatioProtectActionSkipCustom       = "skip_custom"
	RatioProtectActionSkipExpression   = "skip_expression"
	RatioProtectActionSkipNew          = "skip_new"
	RatioProtectActionSkipZero         = "skip_zero"
	RatioProtectActionSkipAnomaly      = "skip_anomaly"
	RatioProtectActionSkipModeMismatch = "skip_mode_mismatch"
)

// RatioProtectChannelSource binds one local channel to exactly one upstream
// price. Selecting the same channel twice is rejected because a channel cannot
// follow two different upstream prices at the same time.
type RatioProtectChannelSource struct {
	ChannelID     int      `json:"channel_id"`
	Source        string   `json:"source"`
	Endpoint      string   `json:"endpoint"`
	UpstreamGroup string   `json:"upstream_group"`
	SyncGroups    []string `json:"sync_groups"`
	MarkupMode    string   `json:"markup_mode"`
	MarkupValue   float64  `json:"markup_value"`
}

// RatioProtectSourceState is the last-seen upstream snapshot of one binding.
// Binding fingerprints the whole binding, so editing it restarts the snapshot.
type RatioProtectSourceState struct {
	Binding        string                        `json:"binding,omitempty"`
	LastSeen       map[string]map[string]float64 `json:"last_seen,omitempty"`
	LastSeenGroups map[string]float64            `json:"last_seen_group_ratio,omitempty"`
}

// RatioProtectMarkup is the pricing rule applied to one binding.
type RatioProtectMarkup struct {
	Mode  string
	Value float64
}

// RatioProtectBinding is one upstream price the task polls: either a single
// channel bound by the administrator, or the legacy multi-channel merge.
type RatioProtectBinding struct {
	Key           string
	ChannelIDs    []int
	Source        string
	Endpoint      string
	UpstreamGroup string
	SyncGroups    []string
	Markup        RatioProtectMarkup
}

type RatioProtectSourceDecisions struct {
	Key       string
	Name      string
	Decisions []RatioProtectDecision
}

// RatioProtectConflict reports a model priced differently by two bindings. The
// first binding wins; the reporter keeps the losing value visible.
type RatioProtectConflict struct {
	Model       string
	Winner      string
	Loser       string
	WinnerValue map[string]float64
	LoserValue  map[string]float64
	Changed     []string
}

type RatioProtectSetting struct {
	Enabled            bool                               `json:"enabled"`
	AutoApply          bool                               `json:"auto_apply"`
	IntervalMinutes    int                                `json:"interval_minutes"`
	ChannelID          int                                `json:"channel_id"`
	ChannelIDs         []int                              `json:"channel_ids"`
	Endpoint           string                             `json:"endpoint"`
	AuthToken          string                             `json:"auth_token"`
	SyncGroups         []string                           `json:"sync_groups"`
	MarkupMode         string                             `json:"markup_mode"`
	MarkupValue        float64                            `json:"markup_value"`
	ProtectModelRatio  bool                               `json:"protect_model_ratio"`
	ProtectModelPrice  bool                               `json:"protect_model_price"`
	ProtectGroupRatio  bool                               `json:"protect_group_ratio"`
	SkipZeroUpstream   bool                               `json:"skip_zero_upstream"`
	MaxChangeFactor    float64                            `json:"max_change_factor"`
	Notify             bool                               `json:"notify"`
	LastSeen           map[string]map[string]float64      `json:"last_seen"`
	LastSeenSource     string                             `json:"last_seen_source"`
	LastSeenGroupRatio map[string]float64                 `json:"last_seen_group_ratio"`
	Sources            []RatioProtectChannelSource        `json:"sources"`
	SourceStates       map[string]RatioProtectSourceState `json:"source_states"`
}

var ratioProtectSetting = RatioProtectSetting{
	Enabled:            false,
	AutoApply:          true,
	IntervalMinutes:    DefaultRatioProtectIntervalMinutes,
	MarkupMode:         RatioProtectModeAdd,
	MarkupValue:        DefaultRatioProtectMarkupValue,
	ProtectModelRatio:  true,
	ProtectModelPrice:  false,
	ProtectGroupRatio:  true,
	SkipZeroUpstream:   true,
	MaxChangeFactor:    DefaultRatioProtectMaxChangeFactor,
	Notify:             true,
	LastSeen:           map[string]map[string]float64{},
	LastSeenGroupRatio: map[string]float64{},
	Sources:            nil,
	SourceStates:       map[string]RatioProtectSourceState{},
}

func init() {
	config.GlobalConfig.Register("ratio_protect_setting", &ratioProtectSetting)
}

func GetRatioProtectSetting() *RatioProtectSetting {
	normalizeRatioProtectSetting(&ratioProtectSetting)
	return &ratioProtectSetting
}

func RatioProtectSourceKey(channelID int, endpoint string) string {
	return fmt.Sprintf("%d|%s", channelID, strings.TrimSpace(endpoint))
}

func RatioProtectSourceKeys(channelIDs []int, endpoint string) string {
	return RatioProtectSourceKeysWithGroups(channelIDs, endpoint, nil)
}

func RatioProtectSourceKeysWithGroups(channelIDs []int, endpoint string, groups []string) string {
	ids := uniqueChannelIDs(channelIDs)
	slices.Sort(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	key := strings.Join(parts, ",") + "|" + strings.TrimSpace(endpoint)
	selected := uniqueSyncGroups(groups)
	if len(selected) == 0 {
		return key
	}
	return key + "|" + strings.Join(selected, ",")
}

func (setting *RatioProtectSetting) SelectedChannelIDs() []int {
	if setting == nil {
		return nil
	}
	if setting.Sources != nil {
		ids := make([]int, 0, len(setting.Sources))
		for _, source := range setting.Sources {
			ids = append(ids, source.ChannelID)
		}
		return ids
	}
	ids := uniqueChannelIDs(setting.ChannelIDs)
	if len(ids) > 0 {
		return ids
	}
	if setting.ChannelID != 0 {
		return []int{setting.ChannelID}
	}
	return nil
}

func (setting *RatioProtectSetting) SelectedSyncGroups() []string {
	if setting == nil {
		return nil
	}
	return uniqueSyncGroups(setting.SyncGroups)
}

func (setting *RatioProtectSetting) HasAuthToken() bool {
	return setting != nil && strings.TrimSpace(setting.AuthToken) != ""
}

// Markup returns the default pricing rule, used by legacy settings and as the
// fallback for a channel source that does not set its own markup.
func (setting *RatioProtectSetting) Markup() RatioProtectMarkup {
	if setting == nil {
		return RatioProtectMarkup{Mode: RatioProtectModeAdd, Value: DefaultRatioProtectMarkupValue}
	}
	return RatioProtectMarkup{Mode: setting.MarkupMode, Value: setting.MarkupValue}
}

// HasChannelSources distinguishes an explicit binding list (including an empty
// list) from an unmigrated configuration, which still uses the legacy channels.
func (setting *RatioProtectSetting) HasChannelSources() bool {
	return setting != nil && setting.Sources != nil
}

// EffectiveBindings lists every upstream price to poll. Each bound channel is
// its own binding so one channel always follows exactly one upstream price;
// legacy settings keep their previous merged multi-channel binding.
func (setting *RatioProtectSetting) EffectiveBindings() []RatioProtectBinding {
	if setting == nil {
		return nil
	}
	if setting.Sources != nil {
		bindings := make([]RatioProtectBinding, 0, len(setting.Sources))
		for _, source := range setting.Sources {
			bindings = append(bindings, RatioProtectBinding{
				Key:           RatioProtectChannelSourceKey(source.ChannelID),
				ChannelIDs:    []int{source.ChannelID},
				Source:        source.Source,
				Endpoint:      source.Endpoint,
				UpstreamGroup: source.UpstreamGroup,
				SyncGroups:    source.SyncGroups,
				Markup:        RatioProtectMarkup{Mode: source.MarkupMode, Value: source.MarkupValue},
			})
		}
		return bindings
	}
	ids := uniqueChannelIDs(setting.ChannelIDs)
	if len(ids) == 0 && setting.ChannelID != 0 {
		ids = []int{setting.ChannelID}
	}
	if len(ids) == 0 {
		return nil
	}
	groups := setting.SelectedSyncGroups()
	endpoint := strings.TrimSpace(setting.Endpoint)
	return []RatioProtectBinding{{
		Key:        RatioProtectSourceKeysWithGroups(ids, endpoint, groups),
		ChannelIDs: ids,
		Source:     RatioProtectSourceAuto,
		Endpoint:   endpoint,
		SyncGroups: groups,
		Markup:     setting.Markup(),
	}}
}

func RatioProtectChannelSourceKey(channelID int) string {
	return "ch:" + strconv.Itoa(channelID)
}

// Fingerprint identifies the binding configuration. Any edit changes it, which
// restarts that binding's last-seen snapshot.
func (binding RatioProtectBinding) Fingerprint() string {
	if binding.Source == RatioProtectSourceAuto && binding.UpstreamGroup == "" {
		return binding.Key
	}
	return strings.Join([]string{
		binding.Key,
		binding.Source,
		strings.TrimSpace(binding.Endpoint),
		strings.TrimSpace(binding.UpstreamGroup),
		strings.Join(uniqueSyncGroups(binding.SyncGroups), ","),
	}, "|")
}

// LoadBindingState returns the last-seen snapshot of one binding. A changed
// binding fingerprint starts from an empty snapshot.
func (setting *RatioProtectSetting) LoadBindingState(binding RatioProtectBinding) RatioProtectSourceState {
	fingerprint := binding.Fingerprint()
	loaded := RatioProtectSourceState{Binding: fingerprint}
	if setting == nil {
		return loaded
	}
	if setting.HasChannelSources() {
		stored, ok := setting.SourceStates[fingerprint]
		if !ok {
			return loaded
		}
		loaded.LastSeen = CloneLastSeen(stored.LastSeen)
		loaded.LastSeenGroups = CloneGroupRatioLastSeen(stored.LastSeenGroups)
		return loaded
	}
	if setting.LastSeenSource != fingerprint {
		return loaded
	}
	loaded.LastSeen = CloneLastSeen(setting.LastSeen)
	loaded.LastSeenGroups = CloneGroupRatioLastSeen(setting.LastSeenGroupRatio)
	return loaded
}

func (state RatioProtectSourceState) WithDecision(decision RatioProtectDecision) RatioProtectSourceState {
	state.LastSeen = ApplyDecisionToLastSeen(CloneLastSeen(state.LastSeen), decision)
	state.LastSeenGroups = ApplyDecisionToGroupRatioLastSeen(CloneGroupRatioLastSeen(state.LastSeenGroups), decision)
	return state
}

func uniqueChannelIDs(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueSyncGroups(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

func normalizeRatioProtectSetting(setting *RatioProtectSetting) {
	if setting.IntervalMinutes < MinRatioProtectIntervalMinutes {
		setting.IntervalMinutes = DefaultRatioProtectIntervalMinutes
	}
	if setting.IntervalMinutes > MaxRatioProtectIntervalMinutes {
		setting.IntervalMinutes = MaxRatioProtectIntervalMinutes
	}
	if setting.MarkupMode != RatioProtectModeMultiply {
		setting.MarkupMode = RatioProtectModeAdd
	}
	if math.IsNaN(setting.MarkupValue) || math.IsInf(setting.MarkupValue, 0) || setting.MarkupValue < 0 {
		setting.MarkupValue = DefaultRatioProtectMarkupValue
	}
	if setting.MarkupMode == RatioProtectModeMultiply && setting.MarkupValue == 0 {
		setting.MarkupValue = 1
	}
	if math.IsNaN(setting.MaxChangeFactor) || math.IsInf(setting.MaxChangeFactor, 0) || setting.MaxChangeFactor < 0 {
		setting.MaxChangeFactor = DefaultRatioProtectMaxChangeFactor
	}
	if !setting.ProtectModelRatio && !setting.ProtectModelPrice && !setting.ProtectGroupRatio {
		setting.ProtectModelRatio = true
	}
	if setting.LastSeen == nil {
		setting.LastSeen = map[string]map[string]float64{}
	}
	if setting.LastSeenGroupRatio == nil {
		setting.LastSeenGroupRatio = map[string]float64{}
	}
	setting.ChannelIDs = uniqueChannelIDs(setting.ChannelIDs)
	if len(setting.ChannelIDs) == 0 && setting.ChannelID != 0 {
		setting.ChannelIDs = []int{setting.ChannelID}
	}
	setting.AuthToken = strings.TrimSpace(setting.AuthToken)
	setting.SyncGroups = uniqueSyncGroups(setting.SyncGroups)
	setting.Sources = normalizeRatioProtectSources(setting.Sources, setting)
	if setting.Sources != nil {
		ids := make([]int, 0, len(setting.Sources))
		for _, source := range setting.Sources {
			ids = append(ids, source.ChannelID)
		}
		setting.ChannelIDs = ids
		setting.ChannelID = 0
		if len(ids) > 0 {
			setting.ChannelID = ids[0]
		}
	}
	setting.SourceStates = pruneRatioProtectSourceStates(setting.SourceStates, setting.EffectiveBindings())
}

func normalizeRatioProtectSources(sources []RatioProtectChannelSource, setting *RatioProtectSetting) []RatioProtectChannelSource {
	if sources == nil {
		return nil
	}
	result := make([]RatioProtectChannelSource, 0, len(sources))
	seen := make(map[int]struct{}, len(sources))
	for _, source := range sources {
		if source.ChannelID == 0 {
			continue
		}
		if _, ok := seen[source.ChannelID]; ok {
			continue
		}
		seen[source.ChannelID] = struct{}{}
		switch source.Source {
		case RatioProtectSourceSub2API:
		default:
			source.Source = RatioProtectSourceNewAPI
		}
		source.Endpoint = strings.TrimSpace(source.Endpoint)
		source.UpstreamGroup = strings.TrimSpace(source.UpstreamGroup)
		source.SyncGroups = uniqueSyncGroups(source.SyncGroups)
		if source.MarkupMode != RatioProtectModeAdd && source.MarkupMode != RatioProtectModeMultiply {
			source.MarkupMode = setting.MarkupMode
			source.MarkupValue = setting.MarkupValue
		} else if math.IsNaN(source.MarkupValue) || math.IsInf(source.MarkupValue, 0) || source.MarkupValue < 0 {
			source.MarkupValue = setting.MarkupValue
		}
		result = append(result, source)
	}
	return result
}

// pruneRatioProtectSourceStates drops snapshots of bindings that no longer
// exist, which is what makes editing a binding restart its snapshot.
func pruneRatioProtectSourceStates(states map[string]RatioProtectSourceState, bindings []RatioProtectBinding) map[string]RatioProtectSourceState {
	if len(states) == 0 {
		return map[string]RatioProtectSourceState{}
	}
	current := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		current[binding.Fingerprint()] = struct{}{}
	}
	result := make(map[string]RatioProtectSourceState, len(states))
	for fingerprint, state := range states {
		if _, ok := current[fingerprint]; !ok {
			continue
		}
		state.Binding = fingerprint
		result[fingerprint] = state
	}
	return result
}

func ValidateRatioProtectOption(key, value string) error {
	switch key {
	case "ratio_protect_setting.enabled",
		"ratio_protect_setting.auto_apply",
		"ratio_protect_setting.protect_model_ratio",
		"ratio_protect_setting.protect_model_price",
		"ratio_protect_setting.protect_group_ratio",
		"ratio_protect_setting.skip_zero_upstream",
		"ratio_protect_setting.notify":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s must be a boolean", key)
		}
	case "ratio_protect_setting.interval_minutes":
		minutes, err := strconv.Atoi(value)
		if err != nil || minutes < MinRatioProtectIntervalMinutes || minutes > MaxRatioProtectIntervalMinutes {
			return fmt.Errorf("interval must be between %d and %d minutes", MinRatioProtectIntervalMinutes, MaxRatioProtectIntervalMinutes)
		}
	case "ratio_protect_setting.channel_id":
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("channel_id must be an integer")
		}
	case "ratio_protect_setting.channel_ids":
		var ids []int
		if err := common.UnmarshalJsonStr(value, &ids); err != nil {
			return fmt.Errorf("channel_ids must be a JSON array of integers")
		}
	case "ratio_protect_setting.sources":
		var sources []RatioProtectChannelSource
		if err := common.UnmarshalJsonStr(value, &sources); err != nil {
			return fmt.Errorf("sources must be a JSON array of channel sources")
		}
		for _, source := range sources {
			if source.Source != RatioProtectSourceAuto && source.Source != RatioProtectSourceNewAPI && source.Source != RatioProtectSourceSub2API {
				return fmt.Errorf("source must be newapi or sub2api")
			}
			if source.MarkupMode != RatioProtectSourceAuto && source.MarkupMode != RatioProtectModeAdd && source.MarkupMode != RatioProtectModeMultiply {
				return fmt.Errorf("markup_mode must be add or multiply")
			}
			if source.MarkupValue < 0 || math.IsNaN(source.MarkupValue) || math.IsInf(source.MarkupValue, 0) {
				return fmt.Errorf("markup_value must be a finite, non-negative number")
			}
		}
	case "ratio_protect_setting.source_states":
		var states map[string]RatioProtectSourceState
		if err := common.UnmarshalJsonStr(value, &states); err != nil {
			return fmt.Errorf("source_states must be a JSON object")
		}
	case "ratio_protect_setting.auth_token":
		return nil
	case "ratio_protect_setting.sync_groups":
		var groups []string
		if err := common.UnmarshalJsonStr(value, &groups); err != nil {
			return fmt.Errorf("sync_groups must be a JSON array of strings")
		}
	case "ratio_protect_setting.endpoint":
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil
		}
		if trimmed != "openrouter" && !strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			return fmt.Errorf("endpoint must be a path, openrouter, or a full URL")
		}
	case "ratio_protect_setting.markup_mode":
		if value != RatioProtectModeAdd && value != RatioProtectModeMultiply {
			return fmt.Errorf("markup_mode must be add or multiply")
		}
	case "ratio_protect_setting.markup_value":
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			return fmt.Errorf("markup_value must be a finite, non-negative number")
		}
	case "ratio_protect_setting.max_change_factor":
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			return fmt.Errorf("max_change_factor must be a finite, non-negative number")
		}
	case "ratio_protect_setting.last_seen":
		var seen map[string]map[string]float64
		if err := common.UnmarshalJsonStr(value, &seen); err != nil {
			return fmt.Errorf("last_seen must be a JSON object")
		}
	case "ratio_protect_setting.last_seen_source":
		return nil
	case "ratio_protect_setting.last_seen_group_ratio":
		var seen map[string]float64
		if err := common.UnmarshalJsonStr(value, &seen); err != nil {
			return fmt.Errorf("last_seen_group_ratio must be a JSON object")
		}
	}
	return nil
}

func RoundProtectValue(value float64) float64 {
	return math.Round(value*1e6) / 1e6
}

func nearlyEqualProtect(a, b float64) bool {
	const epsilon = 1e-9
	if a > b {
		return a-b < epsilon
	}
	return b-a < epsilon
}

func ApplyProtectMarkup(value float64, markup RatioProtectMarkup) float64 {
	switch markup.Mode {
	case RatioProtectModeMultiply:
		if markup.Value <= 0 {
			return RoundProtectValue(value)
		}
		return RoundProtectValue(value * markup.Value)
	default:
		return RoundProtectValue(value + markup.Value)
	}
}

func ProtectFieldEnabled(field string, setting *RatioProtectSetting) bool {
	if setting == nil {
		return false
	}
	switch field {
	case RatioProtectFieldModelRatio:
		return setting.ProtectModelRatio
	case RatioProtectFieldModelPrice:
		return setting.ProtectModelPrice
	case RatioProtectFieldGroupRatio:
		return setting.ProtectGroupRatio
	default:
		return false
	}
}

func clonePricingSyncMap(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	result := make(map[string]any, len(data))
	for key, value := range data {
		if nested, ok := value.(map[string]any); ok {
			copied := make(map[string]any, len(nested))
			for nestedKey, nestedValue := range nested {
				copied[nestedKey] = nestedValue
			}
			result[key] = copied
			continue
		}
		result[key] = value
	}
	return result
}

func pricingSyncFieldMap(data map[string]any, field string) map[string]any {
	if data == nil {
		return nil
	}
	switch typed := data[field].(type) {
	case map[string]any:
		return typed
	case map[string]float64:
		result := make(map[string]any, len(typed))
		for key, value := range typed {
			result[key] = value
		}
		return result
	default:
		return nil
	}
}

func protectNumber(value any, markup RatioProtectMarkup, skipZero bool) (float64, bool) {
	number, ok := asProtectFloat64(value)
	if !ok {
		return 0, false
	}
	if skipZero && number == 0 {
		return number, true
	}
	return ApplyProtectMarkup(number, markup), true
}

func asProtectFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

// ApplyProtectToPricingSyncData returns a copy of upstream sync data with
// the configured markup applied to protectable numeric fields.
func ApplyProtectToPricingSyncData(data map[string]any) map[string]any {
	return ApplyProtectToPricingSyncDataWithSetting(data, GetRatioProtectSetting())
}

func ApplyProtectToPricingSyncDataWithSetting(data map[string]any, setting *RatioProtectSetting) map[string]any {
	if setting == nil || !setting.Enabled || (!setting.ProtectModelRatio && !setting.ProtectModelPrice && !setting.ProtectGroupRatio) {
		return data
	}
	result := clonePricingSyncMap(data)
	for _, field := range []string{RatioProtectFieldModelRatio, RatioProtectFieldModelPrice, RatioProtectFieldGroupRatio} {
		if !ProtectFieldEnabled(field, setting) {
			continue
		}
		entries := pricingSyncFieldMap(result, field)
		if len(entries) == 0 {
			continue
		}
		updated := make(map[string]any, len(entries))
		for name, raw := range entries {
			if protected, ok := protectNumber(raw, setting.Markup(), setting.SkipZeroUpstream); ok {
				updated[name] = protected
				continue
			}
			updated[name] = raw
		}
		result[field] = updated
	}
	return result
}

type RatioProtectDecision struct {
	Name    string
	Action  string
	Raw     map[string]float64
	Target  map[string]float64
	Local   map[string]float64
	Changed []string
}

func extractProtectableFields(data map[string]any, name string, setting *RatioProtectSetting) (map[string]float64, bool, bool) {
	fields := make(map[string]float64)
	if valueMap(data, "billing_mode")[name] == "tiered_expr" {
		return fields, true, true
	}
	for _, field := range []string{RatioProtectFieldModelRatio, RatioProtectFieldModelPrice} {
		if !ProtectFieldEnabled(field, setting) {
			continue
		}
		if value, ok := asProtectFloat64(valueMap(data, field)[name]); ok {
			fields[field] = value
		}
	}
	return fields, false, len(fields) > 0
}

func valueMap(data map[string]any, field string) map[string]any {
	entries := pricingSyncFieldMap(data, field)
	if entries == nil {
		return map[string]any{}
	}
	return entries
}

func sameProtectFields(left, right map[string]float64) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, ok := right[key]
		if !ok || !nearlyEqualProtect(value, other) {
			return false
		}
	}
	return true
}

func applyMarkupToFields(raw map[string]float64, setting *RatioProtectSetting, markup RatioProtectMarkup) map[string]float64 {
	target := make(map[string]float64, len(raw))
	for field, value := range raw {
		if setting.SkipZeroUpstream && value == 0 {
			target[field] = value
			continue
		}
		target[field] = ApplyProtectMarkup(value, markup)
	}
	return target
}

func isAnomalousChange(previous, current map[string]float64, factor float64) bool {
	if factor <= 0 {
		return false
	}
	for field, next := range current {
		prev, ok := previous[field]
		if !ok {
			continue
		}
		if prev == 0 {
			if next > 0 {
				return true
			}
			continue
		}
		ratio := next / prev
		if ratio < 1 {
			ratio = prev / next
		}
		if next == 0 || ratio > factor {
			return true
		}
	}
	return false
}

func changedFields(previous, current map[string]float64) []string {
	seen := make(map[string]struct{})
	var names []string
	for field := range current {
		seen[field] = struct{}{}
		if prev, ok := previous[field]; !ok || !nearlyEqualProtect(prev, current[field]) {
			names = append(names, field)
		}
	}
	for field := range previous {
		if _, ok := seen[field]; !ok {
			names = append(names, field)
		}
	}
	return names
}

func localHasModel(localData map[string]any, name string) bool {
	_, hasRatio := valueMap(localData, RatioProtectFieldModelRatio)[name]
	_, hasPrice := valueMap(localData, RatioProtectFieldModelPrice)[name]
	_, hasExpr := valueMap(localData, "billing_expr")[name]
	return hasRatio || hasPrice || hasExpr
}

// DecideRatioProtectActions compares raw upstream prices with the last-seen
// snapshot. Local customizations are kept until the upstream value itself
// changes; then local is rewritten to upstream plus markup.
func DecideRatioProtectActions(localData, upstreamData map[string]any, lastSeen map[string]map[string]float64, setting *RatioProtectSetting, markup RatioProtectMarkup) []RatioProtectDecision {
	if setting == nil {
		setting = GetRatioProtectSetting()
	}
	if lastSeen == nil {
		lastSeen = map[string]map[string]float64{}
	}
	names := make(map[string]struct{})
	for _, field := range []string{RatioProtectFieldModelRatio, RatioProtectFieldModelPrice, "billing_mode"} {
		for name := range valueMap(upstreamData, field) {
			names[name] = struct{}{}
		}
	}
	decisions := make([]RatioProtectDecision, 0, len(names))
	for name := range names {
		raw, isExpr, hasFields := extractProtectableFields(upstreamData, name, setting)
		localFields, localExpr, _ := extractProtectableFields(localData, name, setting)
		decision := RatioProtectDecision{Name: name, Raw: raw, Local: localFields}
		if isExpr || localExpr {
			decision.Action = RatioProtectActionSkipExpression
			decisions = append(decisions, decision)
			continue
		}
		if !localHasModel(localData, name) {
			decision.Action = RatioProtectActionSkipNew
			decisions = append(decisions, decision)
			continue
		}
		if !hasFields {
			decision.Action = RatioProtectActionSkipModeMismatch
			decisions = append(decisions, decision)
			continue
		}
		if mismatchedProtectMode(localFields, raw) {
			decision.Action = RatioProtectActionSkipModeMismatch
			decisions = append(decisions, decision)
			continue
		}
		if setting.SkipZeroUpstream && allZero(raw) {
			decision.Action = RatioProtectActionSkipZero
			decision.Target = raw
			decisions = append(decisions, decision)
			continue
		}
		target := applyMarkupToFields(raw, setting, markup)
		decision.Target = target
		previous, seen := lastSeen[name]
		if seen && isAnomalousChange(previous, raw, setting.MaxChangeFactor) {
			decision.Action = RatioProtectActionSkipAnomaly
			decision.Changed = changedFields(previous, raw)
			decisions = append(decisions, decision)
			continue
		}
		if !seen {
			if sameProtectFields(localFields, target) {
				decision.Action = RatioProtectActionUnchanged
			} else if sameProtectFields(localFields, raw) {
				decision.Action = RatioProtectActionApply
				decision.Changed = changedFields(raw, target)
			} else {
				decision.Action = RatioProtectActionSkipCustom
			}
			decisions = append(decisions, decision)
			continue
		}
		if sameProtectFields(previous, raw) {
			decision.Action = RatioProtectActionUnchanged
			decisions = append(decisions, decision)
			continue
		}
		decision.Changed = changedFields(previous, raw)
		if sameProtectFields(localFields, target) {
			decision.Action = RatioProtectActionUnchanged
		} else {
			decision.Action = RatioProtectActionApply
		}
		decisions = append(decisions, decision)
	}
	return decisions
}

func mismatchedProtectMode(localFields, raw map[string]float64) bool {
	localRatio := hasOnly(localFields, RatioProtectFieldModelRatio)
	localPrice := hasOnly(localFields, RatioProtectFieldModelPrice)
	rawRatio := hasOnly(raw, RatioProtectFieldModelRatio)
	rawPrice := hasOnly(raw, RatioProtectFieldModelPrice)
	return (localRatio && rawPrice) || (localPrice && rawRatio)
}

func hasOnly(fields map[string]float64, key string) bool {
	if len(fields) != 1 {
		return false
	}
	_, ok := fields[key]
	return ok
}

func allZero(fields map[string]float64) bool {
	if len(fields) == 0 {
		return false
	}
	for _, value := range fields {
		if value != 0 {
			return false
		}
	}
	return true
}

func CloneLastSeen(seen map[string]map[string]float64) map[string]map[string]float64 {
	result := make(map[string]map[string]float64, len(seen))
	for name, fields := range seen {
		copied := make(map[string]float64, len(fields))
		for field, value := range fields {
			copied[field] = value
		}
		result[name] = copied
	}
	return result
}

func ApplyDecisionToLastSeen(seen map[string]map[string]float64, decision RatioProtectDecision) map[string]map[string]float64 {
	if seen == nil {
		seen = map[string]map[string]float64{}
	}
	switch decision.Action {
	case RatioProtectActionSkipNew, RatioProtectActionSkipAnomaly:
		return seen
	}
	if len(decision.Raw) == 0 {
		return seen
	}
	seen[decision.Name] = decision.Raw
	return seen
}

func CloneGroupRatioLastSeen(seen map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(seen))
	maps.Copy(result, seen)
	return result
}

func ApplyDecisionToGroupRatioLastSeen(seen map[string]float64, decision RatioProtectDecision) map[string]float64 {
	if seen == nil {
		seen = map[string]float64{}
	}
	switch decision.Action {
	case RatioProtectActionSkipNew, RatioProtectActionSkipAnomaly:
		return seen
	}
	if raw, ok := decision.Raw[RatioProtectFieldGroupRatio]; ok {
		seen[decision.Name] = raw
	}
	return seen
}

func FilterPricingSyncDataByGroups(data map[string]any, groups []string, modelGroups func(string) []string) map[string]any {
	selected := uniqueSyncGroups(groups)
	if len(selected) == 0 {
		return data
	}
	allowed := make(map[string]struct{}, len(selected))
	for _, group := range selected {
		allowed[group] = struct{}{}
	}
	result := clonePricingSyncMap(data)
	for _, field := range []string{
		RatioProtectFieldModelRatio,
		RatioProtectFieldModelPrice,
		"completion_ratio",
		"cache_ratio",
		"create_cache_ratio",
		"image_ratio",
		"audio_ratio",
		"audio_completion_ratio",
		"billing_mode",
		"billing_expr",
	} {
		entries := pricingSyncFieldMap(result, field)
		if len(entries) == 0 {
			continue
		}
		filtered := make(map[string]any, len(entries))
		for name, value := range entries {
			if modelMatchesProtectGroups(name, allowed, modelGroups) {
				filtered[name] = value
			}
		}
		result[field] = filtered
	}
	groupEntries := pricingSyncFieldMap(result, RatioProtectFieldGroupRatio)
	if len(groupEntries) > 0 {
		filtered := make(map[string]any, len(selected))
		for name, value := range groupEntries {
			if _, ok := allowed[name]; ok {
				filtered[name] = value
			}
		}
		result[RatioProtectFieldGroupRatio] = filtered
	}
	return result
}

func modelMatchesProtectGroups(name string, allowed map[string]struct{}, modelGroups func(string) []string) bool {
	if len(allowed) == 0 {
		return true
	}
	if modelGroups == nil {
		return true
	}
	groups := modelGroups(name)
	if len(groups) == 0 {
		return false
	}
	for _, group := range groups {
		if group == "all" {
			return true
		}
		if _, ok := allowed[group]; ok {
			return true
		}
	}
	return false
}

func groupRatioValueMap(data map[string]any) map[string]float64 {
	entries := pricingSyncFieldMap(data, RatioProtectFieldGroupRatio)
	result := make(map[string]float64, len(entries))
	for name, raw := range entries {
		if value, ok := asProtectFloat64(raw); ok {
			result[name] = value
		}
	}
	return result
}

func localHasGroupRatio(localData map[string]any, name string) bool {
	_, ok := groupRatioValueMap(localData)[name]
	return ok
}

func DecideGroupRatioProtectActions(localData, upstreamData map[string]any, lastSeen map[string]float64, setting *RatioProtectSetting, markup RatioProtectMarkup) []RatioProtectDecision {
	if setting == nil {
		setting = GetRatioProtectSetting()
	}
	if !setting.ProtectGroupRatio {
		return nil
	}
	if lastSeen == nil {
		lastSeen = map[string]float64{}
	}
	rawValues := groupRatioValueMap(upstreamData)
	localValues := groupRatioValueMap(localData)
	names := make([]string, 0, len(rawValues))
	for name := range rawValues {
		names = append(names, name)
	}
	slices.Sort(names)
	decisions := make([]RatioProtectDecision, 0, len(names))
	for _, name := range names {
		rawValue := rawValues[name]
		raw := map[string]float64{RatioProtectFieldGroupRatio: rawValue}
		localFields := map[string]float64{}
		if localValue, ok := localValues[name]; ok {
			localFields[RatioProtectFieldGroupRatio] = localValue
		}
		decision := RatioProtectDecision{Name: name, Raw: raw, Local: localFields}
		if !localHasGroupRatio(localData, name) {
			decision.Action = RatioProtectActionSkipNew
			decisions = append(decisions, decision)
			continue
		}
		if setting.SkipZeroUpstream && rawValue == 0 {
			decision.Action = RatioProtectActionSkipZero
			decision.Target = raw
			decisions = append(decisions, decision)
			continue
		}
		target := applyMarkupToFields(raw, setting, markup)
		decision.Target = target
		previousValue, seen := lastSeen[name]
		var previous map[string]float64
		if seen {
			previous = map[string]float64{RatioProtectFieldGroupRatio: previousValue}
		}
		if seen && isAnomalousChange(previous, raw, setting.MaxChangeFactor) {
			decision.Action = RatioProtectActionSkipAnomaly
			decision.Changed = changedFields(previous, raw)
			decisions = append(decisions, decision)
			continue
		}
		if !seen {
			if sameProtectFields(localFields, target) {
				decision.Action = RatioProtectActionUnchanged
			} else if sameProtectFields(localFields, raw) {
				decision.Action = RatioProtectActionApply
				decision.Changed = changedFields(raw, target)
			} else {
				decision.Action = RatioProtectActionSkipCustom
			}
			decisions = append(decisions, decision)
			continue
		}
		if sameProtectFields(previous, raw) {
			decision.Action = RatioProtectActionUnchanged
			decisions = append(decisions, decision)
			continue
		}
		decision.Changed = changedFields(previous, raw)
		if sameProtectFields(localFields, target) {
			decision.Action = RatioProtectActionUnchanged
		} else {
			decision.Action = RatioProtectActionApply
		}
		decisions = append(decisions, decision)
	}
	return decisions
}

// MergeRatioProtectSourceDecisions keeps the first binding as the winner for
// every model, so a model is always priced by exactly one upstream price. A
// later binding that would write a different value is reported as a conflict
// instead of silently overwriting the winner.
func MergeRatioProtectSourceDecisions(sources []RatioProtectSourceDecisions) ([]RatioProtectDecision, map[string]string, []RatioProtectConflict) {
	decisions := make([]RatioProtectDecision, 0)
	winners := make(map[string]string)
	winnerNames := make(map[string]string)
	conflicts := make([]RatioProtectConflict, 0)
	positions := make(map[string]int)
	for _, source := range sources {
		for _, decision := range source.Decisions {
			position, seen := positions[decision.Name]
			if !seen {
				positions[decision.Name] = len(decisions)
				decisions = append(decisions, decision)
				winners[decision.Name] = source.Key
				winnerNames[decision.Name] = source.Name
				continue
			}
			winner := decisions[position]
			changed := changedFields(winner.Target, decision.Target)
			if len(changed) == 0 {
				continue
			}
			conflicts = append(conflicts, RatioProtectConflict{
				Model:       decision.Name,
				Winner:      winnerNames[decision.Name],
				Loser:       source.Name,
				WinnerValue: cloneProtectFields(winner.Target),
				LoserValue:  cloneProtectFields(decision.Target),
				Changed:     changed,
			})
		}
	}
	return decisions, winners, conflicts
}

func cloneProtectFields(fields map[string]float64) map[string]float64 {
	if len(fields) == 0 {
		return nil
	}
	cloned := make(map[string]float64, len(fields))
	maps.Copy(cloned, fields)
	return cloned
}
