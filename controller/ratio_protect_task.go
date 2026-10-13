package controller

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

const ratioProtectNotifyMaxDetails = 12

type ratioProtectTaskPayload struct {
	Manual bool `json:"manual,omitempty"`
}

type ratioProtectTaskSummary struct {
	Checked        int            `json:"checked"`
	Applied        int            `json:"applied"`
	Unchanged      int            `json:"unchanged"`
	Skipped        int            `json:"skipped"`
	Failed         int            `json:"failed"`
	AppliedModels  []string       `json:"applied_models,omitempty"`
	AppliedGroups  []string       `json:"applied_groups,omitempty"`
	SkippedReasons map[string]int `json:"skipped_reasons,omitempty"`
	Source         string         `json:"source,omitempty"`
	FailedSources  []string       `json:"failed_sources,omitempty"`
	Conflicts      []string       `json:"conflicts,omitempty"`
	Notes          []string       `json:"notes,omitempty"`
}

// ratioProtectBindingData is one polled upstream price with its decisions.
type ratioProtectBindingData struct {
	Binding ratio_setting.RatioProtectBinding
	Label   string
	Data    map[string]any
	Notes   []string
	Models  []ratio_setting.RatioProtectDecision
	Groups  []ratio_setting.RatioProtectDecision
}

type ratioProtectHandler struct{}

func (ratioProtectHandler) Type() string { return model.SystemTaskTypeRatioProtect }

func (ratioProtectHandler) Enabled() bool {
	return ratio_setting.GetRatioProtectSetting().Enabled
}

func (ratioProtectHandler) Interval() time.Duration {
	minutes := ratio_setting.GetRatioProtectSetting().IntervalMinutes
	if minutes < ratio_setting.MinRatioProtectIntervalMinutes {
		minutes = ratio_setting.DefaultRatioProtectIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func (ratioProtectHandler) NewPayload() any { return ratioProtectTaskPayload{} }

func (ratioProtectHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := ratioProtectTaskPayload{}
	if err := task.DecodePayload(&payload); err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	summary, err := runRatioProtectTaskOnce(ctx, payload.Manual, service.NewSystemTaskProgressReporter(task, runnerID))
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, summary, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

func runRatioProtectTaskOnce(ctx context.Context, _ bool, report func(processed, total int)) (*ratioProtectTaskSummary, error) {
	setting := ratio_setting.GetRatioProtectSetting()
	summary := &ratioProtectTaskSummary{SkippedReasons: map[string]int{}}
	if report != nil {
		report(0, 1)
	}
	bindings := setting.EffectiveBindings()
	if len(bindings) == 0 {
		return summary, fmt.Errorf("select an upstream channel before enabling ratio protection")
	}
	labels := ratioProtectBindingLabels(bindings)
	local := getLocalPricingSyncData()
	modelLocal := effectivePricingSyncData(local)

	fetched := make([]ratioProtectBindingData, 0, len(bindings))
	failures := make([]string, 0)
	for _, binding := range bindings {
		data, notes, err := fetchRatioProtectBinding(ctx, setting, binding)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s：%s", labels[binding.Key], err.Error()))
			continue
		}
		fetched = append(fetched, ratioProtectBindingData{Binding: binding, Label: labels[binding.Key], Data: data, Notes: notes})
	}
	if len(fetched) == 0 {
		return summary, fmt.Errorf("all protect sources failed: %s", strings.Join(failures, "; "))
	}
	if len(failures) > 0 {
		summary.FailedSources = failures
		logger.LogWarn(ctx, "ratio protect skipped failed sources: "+strings.Join(failures, "; "))
	}

	sourceLabels := make([]string, 0, len(fetched))
	states := make(map[string]ratio_setting.RatioProtectSourceState, len(fetched))
	modelSources := make([]ratio_setting.RatioProtectSourceDecisions, 0, len(fetched))
	groupSources := make([]ratio_setting.RatioProtectSourceDecisions, 0, len(fetched))
	for index := range fetched {
		item := &fetched[index]
		sourceLabels = append(sourceLabels, item.Label)
		summary.Notes = append(summary.Notes, item.Notes...)
		state := setting.LoadBindingState(item.Binding)
		item.Models = ratio_setting.DecideRatioProtectActions(modelLocal, effectivePricingSyncData(item.Data), state.LastSeen, setting, item.Binding.Markup)
		item.Groups = ratio_setting.DecideGroupRatioProtectActions(local, item.Data, state.LastSeenGroups, setting, item.Binding.Markup)
		modelSources = append(modelSources, ratio_setting.RatioProtectSourceDecisions{Key: item.Binding.Key, Name: item.Label, Decisions: item.Models})
		groupSources = append(groupSources, ratio_setting.RatioProtectSourceDecisions{Key: item.Binding.Key, Name: item.Label, Decisions: item.Groups})
		states[item.Binding.Fingerprint()] = state
	}
	summary.Source = strings.Join(sourceLabels, ", ")

	decisions, modelWinners, modelConflicts := ratio_setting.MergeRatioProtectSourceDecisions(modelSources)
	groupDecisions, groupWinners, groupConflicts := ratio_setting.MergeRatioProtectSourceDecisions(groupSources)
	summary.Checked = len(decisions) + len(groupDecisions)
	summary.Conflicts = ratioProtectConflictMessages(append(modelConflicts, groupConflicts...))
	if report != nil {
		report(0, max(summary.Checked, 1))
	}

	snapshot, err := model.GetModelPricingSnapshot(nil)
	if err != nil {
		return summary, err
	}
	versions := make(map[string]string, len(snapshot.Entries))
	configured := make(map[string]model.PricingValues, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		versions[entry.ModelName] = entry.Version
		configured[entry.ModelName] = entry.Configured
	}

	changes := make([]model.ModelPricingChange, 0)
	writtenModels := make(map[string]bool)
	for _, decision := range decisions {
		switch decision.Action {
		case ratio_setting.RatioProtectActionApply:
			if !setting.AutoApply {
				summary.Skipped++
				summary.SkippedReasons["auto_apply_disabled"]++
				continue
			}
			change, applyErr := buildRatioProtectPricingChange(decision, versions, configured)
			if applyErr != nil {
				summary.Failed++
				logger.LogWarn(ctx, "ratio protect skip "+decision.Name+": "+applyErr.Error())
				continue
			}
			changes = append(changes, change)
			writtenModels[decision.Name] = true
			summary.AppliedModels = append(summary.AppliedModels, decision.Name)
		case ratio_setting.RatioProtectActionUnchanged:
			summary.Unchanged++
		default:
			summary.Skipped++
			summary.SkippedReasons[decision.Action]++
		}
	}
	if len(changes) > 0 {
		if err := model.UpdateModelPricing(changes); err != nil {
			return summary, err
		}
		summary.Applied += len(changes)
	}

	writtenGroups := make(map[string]bool)
	nextGroupRatio := ratio_setting.GetGroupRatioCopy()
	groupChanged := false
	for _, decision := range groupDecisions {
		switch decision.Action {
		case ratio_setting.RatioProtectActionApply:
			if !setting.AutoApply {
				summary.Skipped++
				summary.SkippedReasons["auto_apply_disabled"]++
				continue
			}
			target, ok := decision.Target[ratio_setting.RatioProtectFieldGroupRatio]
			if !ok {
				summary.Failed++
				continue
			}
			nextGroupRatio[decision.Name] = target
			writtenGroups[decision.Name] = true
			summary.AppliedGroups = append(summary.AppliedGroups, decision.Name)
			groupChanged = true
		case ratio_setting.RatioProtectActionUnchanged:
			summary.Unchanged++
		default:
			summary.Skipped++
			summary.SkippedReasons[decision.Action]++
		}
	}
	if groupChanged {
		encoded, marshalErr := common.Marshal(nextGroupRatio)
		if marshalErr != nil {
			return summary, marshalErr
		}
		if err := model.UpdateOption("GroupRatio", string(encoded)); err != nil {
			return summary, err
		}
		summary.Applied += len(writtenGroups)
	}

	nextStates := make(map[string]ratio_setting.RatioProtectSourceState, len(fetched))
	for _, item := range fetched {
		fingerprint := item.Binding.Fingerprint()
		state := states[fingerprint]
		for _, decision := range item.Models {
			if !ratioProtectDecisionWritten(decision, writtenModels, item.Binding.Key, modelWinners) {
				continue
			}
			state = state.WithDecision(decision)
		}
		for _, decision := range item.Groups {
			if !ratioProtectDecisionWritten(decision, writtenGroups, item.Binding.Key, groupWinners) {
				continue
			}
			state = state.WithDecision(decision)
		}
		nextStates[fingerprint] = state
	}
	if err := persistRatioProtectStates(setting, nextStates); err != nil {
		logger.LogWarn(ctx, "failed to persist ratio protect snapshot: "+err.Error())
	}
	if report != nil {
		report(summary.Checked, max(summary.Checked, 1))
	}
	if setting.Notify && summary.Applied > 0 {
		service.NotifyRootUser(dto.NotifyTypeChannelUpdate, "上游倍率保护已跟随", buildRatioProtectNotification(summary))
	}
	return summary, nil
}

// fetchRatioProtectBinding polls one binding and merges the payloads of its
// channels. A binding bound to one channel is already a single upstream price.
func fetchRatioProtectBinding(ctx context.Context, setting *ratio_setting.RatioProtectSetting, binding ratio_setting.RatioProtectBinding) (map[string]any, []string, error) {
	endpoint := strings.TrimSpace(binding.Endpoint)
	if endpoint == "" && binding.Source == ratio_setting.RatioProtectSourceSub2API {
		endpoint = ratio_setting.RatioProtectSub2APIDefaultEndpoint
	}
	upstreams := make([]dto.UpstreamDTO, 0, len(binding.ChannelIDs))
	for _, channelID := range binding.ChannelIDs {
		upstream, err := resolveProtectUpstreamDTO(channelID, endpoint)
		if err != nil {
			return nil, nil, err
		}
		upstreams = append(upstreams, upstream)
	}
	options := upstreamFetchOptions{
		Source:         binding.Source,
		UpstreamGroup:  binding.UpstreamGroup,
		AuthToken:      strings.TrimSpace(setting.AuthToken),
		SkipChannelKey: binding.Source == ratio_setting.RatioProtectSourceSub2API,
	}
	client := newUpstreamRatioHTTPClient()
	sources := make([]map[string]any, 0, len(upstreams))
	notes := make([]string, 0, 2)
	var failures []string
	for _, upstream := range upstreams {
		data, err := fetchUpstreamPriceData(ctx, client, upstream, defaultTimeoutSeconds, options)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s(%d)：%s", upstream.Name, upstream.ID, err.Error()))
			continue
		}
		sources = append(sources, data.Data)
		notes = append(notes, data.Notes...)
	}
	if len(sources) == 0 {
		return nil, nil, fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	if len(failures) > 0 {
		logger.LogWarn(ctx, "ratio protect skipped failed channels: "+strings.Join(failures, "; "))
	}
	merged := mergeProtectUpstreamData(sources)
	return ratio_setting.FilterPricingSyncDataByGroups(merged, binding.SyncGroups, model.GetModelEnableGroups), notes, nil
}

// ratioProtectDecisionWritten reports whether a decision may advance its
// last-seen snapshot. An applied decision only counts once it was written by
// the binding that owns the model.
func ratioProtectDecisionWritten(decision ratio_setting.RatioProtectDecision, written map[string]bool, bindingKey string, winners map[string]string) bool {
	if decision.Action != ratio_setting.RatioProtectActionApply {
		return true
	}
	return written[decision.Name] && winners[decision.Name] == bindingKey
}

func ratioProtectBindingLabels(bindings []ratio_setting.RatioProtectBinding) map[string]string {
	ids := make([]int, 0)
	for _, binding := range bindings {
		ids = append(ids, binding.ChannelIDs...)
	}
	names := map[int]string{}
	if len(ids) > 0 {
		if channels, err := model.GetChannelsByIds(ids); err == nil {
			for _, channel := range channels {
				names[channel.Id] = channel.Name
			}
		}
	}
	labels := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		parts := make([]string, 0, len(binding.ChannelIDs))
		for _, id := range binding.ChannelIDs {
			switch id {
			case officialRatioPresetID:
				parts = append(parts, officialRatioPresetName)
			case modelsDevPresetID:
				parts = append(parts, modelsDevPresetName)
			default:
				name := names[id]
				if name == "" {
					name = strconv.Itoa(id)
				}
				parts = append(parts, fmt.Sprintf("%s(%d)", name, id))
			}
		}
		label := strings.Join(parts, ", ")
		if binding.UpstreamGroup != "" {
			label += " · " + binding.UpstreamGroup
		}
		labels[binding.Key] = label
	}
	return labels
}

func ratioProtectConflictMessages(conflicts []ratio_setting.RatioProtectConflict) []string {
	if len(conflicts) == 0 {
		return nil
	}
	messages := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		messages = append(messages, fmt.Sprintf("%s：%s 给出 %s，%s 给出 %s，保留前者",
			conflict.Model,
			conflict.Winner,
			formatProtectValues(conflict.WinnerValue, conflict.Changed),
			conflict.Loser,
			formatProtectValues(conflict.LoserValue, conflict.Changed),
		))
	}
	if len(messages) > ratioProtectNotifyMaxDetails {
		messages = append(messages[:ratioProtectNotifyMaxDetails], fmt.Sprintf("其余 %d 个冲突已省略", len(messages)-ratioProtectNotifyMaxDetails))
	}
	return messages
}

func formatProtectValues(values map[string]float64, fields []string) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, fmt.Sprintf("%s=%g", field, values[field]))
	}
	if len(parts) == 0 {
		return "无价格"
	}
	return strings.Join(parts, ", ")
}

func buildRatioProtectPricingChange(decision ratio_setting.RatioProtectDecision, versions map[string]string, configured map[string]model.PricingValues) (model.ModelPricingChange, error) {
	version, ok := versions[decision.Name]
	if !ok || version == "" {
		return model.ModelPricingChange{}, fmt.Errorf("missing pricing version")
	}
	pricing := model.PricingValues{}
	for key, value := range configured[decision.Name] {
		pricing[key] = value
	}
	for field, value := range decision.Target {
		switch field {
		case ratio_setting.RatioProtectFieldModelRatio:
			pricing["ModelRatio"] = value
			delete(pricing, "ModelPrice")
		case ratio_setting.RatioProtectFieldModelPrice:
			pricing["ModelPrice"] = value
			delete(pricing, "ModelRatio")
		}
	}
	if len(pricing) == 0 {
		return model.ModelPricingChange{}, fmt.Errorf("empty pricing draft")
	}
	return model.ModelPricingChange{
		ModelName:       decision.Name,
		ExpectedVersion: version,
		Pricing:         pricing,
	}, nil
}

// persistRatioProtectStates stores the snapshot of every binding. Bindings that
// were not polled keep the snapshot they already have.
func persistRatioProtectStates(setting *ratio_setting.RatioProtectSetting, states map[string]ratio_setting.RatioProtectSourceState) error {
	if setting.HasChannelSources() {
		merged := make(map[string]ratio_setting.RatioProtectSourceState, len(states)+len(setting.SourceStates))
		for fingerprint, state := range setting.SourceStates {
			merged[fingerprint] = state
		}
		for fingerprint, state := range states {
			merged[fingerprint] = state
		}
		encoded, err := common.Marshal(merged)
		if err != nil {
			return err
		}
		return model.UpdateOption("ratio_protect_setting.source_states", string(encoded))
	}
	for fingerprint, state := range states {
		encoded, err := common.Marshal(state.LastSeen)
		if err != nil {
			return err
		}
		if err := model.UpdateOption("ratio_protect_setting.last_seen", string(encoded)); err != nil {
			return err
		}
		encodedGroups, err := common.Marshal(state.LastSeenGroups)
		if err != nil {
			return err
		}
		if err := model.UpdateOption("ratio_protect_setting.last_seen_group_ratio", string(encodedGroups)); err != nil {
			return err
		}
		return model.UpdateOption("ratio_protect_setting.last_seen_source", fingerprint)
	}
	return nil
}

func buildRatioProtectNotification(summary *ratioProtectTaskSummary) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf(
		"上游倍率保护巡检：检查 %d 项，已跟随 %d 项，未变 %d 项，跳过 %d 项，失败 %d 项。",
		summary.Checked,
		summary.Applied,
		summary.Unchanged,
		summary.Skipped,
		summary.Failed,
	))
	if summary.Source != "" {
		builder.WriteString("\n监听源：" + summary.Source)
	}
	if len(summary.AppliedModels) > 0 {
		display := min(len(summary.AppliedModels), ratioProtectNotifyMaxDetails)
		builder.WriteString(fmt.Sprintf("\n已跟随模型：%s", strings.Join(summary.AppliedModels[:display], ", ")))
		if len(summary.AppliedModels) > display {
			builder.WriteString(fmt.Sprintf("（其余 %d 个已省略）", len(summary.AppliedModels)-display))
		}
	}
	if len(summary.AppliedGroups) > 0 {
		display := min(len(summary.AppliedGroups), ratioProtectNotifyMaxDetails)
		builder.WriteString(fmt.Sprintf("\n已跟随分组：%s", strings.Join(summary.AppliedGroups[:display], ", ")))
		if len(summary.AppliedGroups) > display {
			builder.WriteString(fmt.Sprintf("（其余 %d 个已省略）", len(summary.AppliedGroups)-display))
		}
	}
	if len(summary.Conflicts) > 0 {
		builder.WriteString("\n价格冲突：" + strings.Join(summary.Conflicts, "；"))
	}
	if len(summary.FailedSources) > 0 {
		builder.WriteString("\n失败源：" + strings.Join(summary.FailedSources, "；"))
	}
	if len(summary.Notes) > 0 {
		builder.WriteString("\n说明：" + strings.Join(summary.Notes, "；"))
	}
	return builder.String()
}

func EnqueueRatioProtectTask() (*model.SystemTask, bool, error) {
	return service.EnqueueSystemTask(model.SystemTaskTypeRatioProtect, ratioProtectTaskPayload{Manual: true})
}

func TriggerRatioProtectTask(c *gin.Context) {
	task, created, err := EnqueueRatioProtectTask()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !created {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": "已有上游倍率保护任务正在运行或等待中，不能启动本次手动任务",
			"data": gin.H{
				"task_id": task.TaskID,
				"status":  task.Status,
				"type":    task.Type,
			},
		})
		return
	}
	recordManageAudit(c, "ratio_protect.trigger", map[string]any{
		"task_id": task.TaskID,
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"task_id": task.TaskID,
			"status":  task.Status,
		},
	})
}
