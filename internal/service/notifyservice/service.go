package notifyservice

import (
	"fmt"
	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/internal/service/syncservice"
)

type OutboundMessage struct {
	ChatID int64
	Text   string
}

// ResolveTargetChatIDs 解析最终通知目标：优先配置中的目标用户，回退到消息来源会话。
// 这样做的原因是将通知目标策略集中管理，避免在入口层散落条件分支。
func ResolveTargetChatIDs(config Entity.Config, fallbackChatID int64) []int64 {
	if len(config.TargetUserList) > 0 {
		return config.TargetUserList
	}
	return []int64{fallbackChatID}
}

// BuildArchiveResponse 生成归档阶段通知文案（成功/失败）。
// 这样做的原因是将通知文案规则独立出来，便于统一修改与测试。
func BuildArchiveResponse(ok bool, sourceLink, message string) string {
	if ok {
		return fmt.Sprintf("%s\n消息已备案至: %s!", sourceLink, message)
	}
	return fmt.Sprintf("%s\n消息备份出现异常: %s!", sourceLink, message)
}

// BuildSyncNotifications 根据同步判定与分发结果生成同步阶段通知文案列表。
// 这样做的原因是把“同步结果解释”为用户可读文本的逻辑从入口层剥离。
func BuildSyncNotifications(syncEnabled bool, syncReason string, results []syncservice.DispatchResult) []string {
	if !syncEnabled {
		return []string{syncReason}
	}

	notifications := make([]string, 0, len(results))
	for _, result := range results {
		if result.Success {
			suffix := ""
			if result.Truncated {
				suffix = "，文本已截断"
			}
			if result.ImageRequested && result.UsedImage {
				// 图片全部成功时 ErrorMessage 为空；非空说明线程续发或部分图片失败。
				if result.ErrorMessage != "" {
					notifications = append(notifications, fmt.Sprintf("消息已同步至 %s，并附带图片，但存在部分失败%s: %s", result.Platform, suffix, result.ErrorMessage))
					continue
				}
				notifications = append(notifications, fmt.Sprintf("消息已同步至 %s，并附带图片%s!", result.Platform, suffix))
				continue
			}
			if result.ImageRequested && !result.UsedImage {
				if result.ErrorMessage != "" {
					notifications = append(notifications, fmt.Sprintf("消息已同步至 %s，但图片已跳过%s: %s", result.Platform, suffix, result.ErrorMessage))
					continue
				}
				notifications = append(notifications, fmt.Sprintf("消息已同步至 %s，但图片已跳过%s", result.Platform, suffix))
				continue
			}
			notifications = append(notifications, fmt.Sprintf("消息已同步至 %s%s!", result.Platform, suffix))
			continue
		}
		if result.ErrorMessage != "" {
			notifications = append(notifications, fmt.Sprintf("同步 %s 失败: %s", result.Platform, result.ErrorMessage))
			continue
		}
		notifications = append(notifications, fmt.Sprintf("同步 %s 失败", result.Platform))
	}

	return notifications
}

// BuildAlbumSyncNotifications 生成相册聚合同步通知：复用单消息同步文案，并补充来源链接与相册规模。
// 这样做的原因是相册投递发生在静默窗口之后，通知需要能独立说明是哪一组消息。
func BuildAlbumSyncNotifications(sourceLink string, imageCount int, results []syncservice.DispatchResult) []string {
	lines := BuildSyncNotifications(true, "", results)
	if len(lines) == 0 {
		return nil
	}

	prefix := "相册消息"
	if imageCount > 0 {
		prefix = fmt.Sprintf("相册消息（%d 张图）", imageCount)
	}
	if sourceLink != "" {
		prefix = sourceLink + "\n" + prefix
	}

	for i := range lines {
		lines[i] = prefix + "\n" + lines[i]
	}
	return lines
}

// BuildOutboundMessages 将“目标聊天ID”与“通知文案列表”展开为最终待发送消息序列。
// 这样做的原因是固定发送顺序并分离发送前编排逻辑，使后续同步/异步发送都可复用同一输入。
// 把“要发给谁”和“要发什么内容”做一次笛卡尔展开，生成最终待发送消息列表。
// 目标聊天 ID 列表（例如 [1,2]）和通知文案列表（例如 ["A", "B"]）会被展开为：
// [
//
//	{ChatID: 1, Text: "A"},
//	{ChatID: 1, Text: "B"},
//	{ChatID: 2, Text: "A"},
//	{ChatID: 2, Text: "B"},
//
// ]
func BuildOutboundMessages(chatIDs []int64, archiveResponse string, syncNotifications []string) []OutboundMessage {
	outbound := make([]OutboundMessage, 0, len(chatIDs)*(1+len(syncNotifications)))
	for _, chatID := range chatIDs {
		outbound = append(outbound, OutboundMessage{ChatID: chatID, Text: archiveResponse})
		for _, text := range syncNotifications {
			outbound = append(outbound, OutboundMessage{ChatID: chatID, Text: text})
		}
	}
	return outbound
}
