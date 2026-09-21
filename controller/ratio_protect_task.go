package controller

import (
	"context"
	"fmt"
	"net/http"
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
	channelIDs := setting.SelectedChannelIDs()
	if len(channelIDs) == 0 {
		return summary, fmt.Errorf("select an upstream channel before enabling ratio protection")
	}
	upstream, sources, err := fetchProtectUpstreamData(ctx, setting)
	if err != nil {
		return summary, err
	}
	local := getLocalPricingSyncData()
	sourceNames := make([]string, 0, len(sources))
	for _, source := range sources {
		sourceNames = append(sourceNames, fmt.Sprintf("%s(%d)", source.Name, source.ID))
	}
	summary.Source = strings.Join(sourceNames, ", ")
	sourceKey := ratio_setting.RatioProtectSourceKeysWithGroups(channelIDs, strings.TrimSpace(setting.Endpoint), setting.SelectedSyncGroups())
	lastSeen := ratio_setting.CloneLastSeen(setting.LastSeen)
	lastSeenGroups := ratio_setting.CloneGroupRatioLastSeen(setting.LastSeenGroupRatio)
	if setting.LastSeenSource != "" && setting.LastSeenSource != sourceKey {
		lastSeen = map[string]map[string]float64{}
		lastSeenGroups = map[string]float64{}
	}
	modelLocal := effectivePricingSyncData(local)
	decisions := ratio_setting.DecideRatioProtectActions(modelLocal, effectivePricingSyncData(upstream), lastSeen, setting)
	groupDecisions := ratio_setting.DecideGroupRatioProtectActions(local, upstream, lastSeenGroups, setting)
	summary.Checked = len(decisions) + len(groupDecisions)
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
	appliedDecisions := make([]ratio_setting.RatioProtectDecision, 0)
	nextSeen := ratio_setting.CloneLastSeen(lastSeen)
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
			appliedDecisions = append(appliedDecisions, decision)
			summary.AppliedModels = append(summary.AppliedModels, decision.Name)
		case ratio_setting.RatioProtectActionUnchanged:
			summary.Unchanged++
			nextSeen = ratio_setting.ApplyDecisionToLastSeen(nextSeen, decision)
		default:
			summary.Skipped++
			summary.SkippedReasons[decision.Action]++
			nextSeen = ratio_setting.ApplyDecisionToLastSeen(nextSeen, decision)
		}
	}
	if len(changes) > 0 {
		if err := model.UpdateModelPricing(changes); err != nil {
			return summary, err
		}
		summary.Applied += len(changes)
		for _, decision := range appliedDecisions {
			nextSeen = ratio_setting.ApplyDecisionToLastSeen(nextSeen, decision)
		}
	}

	nextGroupSeen := ratio_setting.CloneGroupRatioLastSeen(lastSeenGroups)
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
			nextGroupSeen = ratio_setting.ApplyDecisionToGroupRatioLastSeen(nextGroupSeen, decision)
			summary.AppliedGroups = append(summary.AppliedGroups, decision.Name)
			groupChanged = true
		case ratio_setting.RatioProtectActionUnchanged:
			summary.Unchanged++
			nextGroupSeen = ratio_setting.ApplyDecisionToGroupRatioLastSeen(nextGroupSeen, decision)
		default:
			summary.Skipped++
			summary.SkippedReasons[decision.Action]++
			nextGroupSeen = ratio_setting.ApplyDecisionToGroupRatioLastSeen(nextGroupSeen, decision)
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
		summary.Applied += len(summary.AppliedGroups)
	}
	if err := persistRatioProtectLastSeen(nextSeen, nextGroupSeen, sourceKey); err != nil {
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

func persistRatioProtectLastSeen(seen map[string]map[string]float64, groupSeen map[string]float64, sourceKey string) error {
	encoded, err := common.Marshal(seen)
	if err != nil {
		return err
	}
	if err := model.UpdateOption("ratio_protect_setting.last_seen", string(encoded)); err != nil {
		return err
	}
	encodedGroups, err := common.Marshal(groupSeen)
	if err != nil {
		return err
	}
	if err := model.UpdateOption("ratio_protect_setting.last_seen_group_ratio", string(encodedGroups)); err != nil {
		return err
	}
	return model.UpdateOption("ratio_protect_setting.last_seen_source", sourceKey)
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
