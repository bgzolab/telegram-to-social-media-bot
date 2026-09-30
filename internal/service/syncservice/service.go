package syncservice

import (
	"fmt"
	"os"
	"path/filepath"
	"telegram-message-sync-bot/internal/Database"
	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/pkg/LogUtils"
	"telegram-message-sync-bot/pkg/StrUtils"
	"time"
)

type ImagePayload struct {
	FilePath string
}

type Payload struct {
	Text   string
	Image  *ImagePayload
	Images []ImagePayload
}

// ImagePaths 返回载荷内去重后的图片路径列表，兼容单图与多图两种装配方式。
func (p Payload) ImagePaths() []string {
	paths := make([]string, 0, 1+len(p.Images))
	seen := make(map[string]struct{}, 1+len(p.Images))

	appendPath := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	if p.Image != nil {
		appendPath(p.Image.FilePath)
	}
	for _, image := range p.Images {
		appendPath(image.FilePath)
	}

	return paths
}

type Sender interface {
	Name() string
	Send(config Entity.Config, payload Payload) DispatchResult
}

type DispatchResult struct {
	Platform       string
	Success        bool
	ImageRequested bool
	UsedImage      bool
	Truncated      bool
	RemoteID       string
	RemoteURL      string
	ErrorMessage   string
}

type DispatchTrigger string

const (
	DispatchTriggerAutomatic DispatchTrigger = "automatic"
	DispatchTriggerManual    DispatchTrigger = "manual"
)

func BuildPayload(text string, imagePath string) Payload {
	payload := Payload{Text: StrUtils.UnescapeHashtags(text)}
	if imagePath == "" {
		return payload
	}

	if _, err := os.Stat(imagePath); err != nil {
		return payload
	}

	payload.Image = &ImagePayload{FilePath: imagePath}
	return payload
}

// BuildPayloadWithImages 构造多图载荷，自动过滤不存在的本地文件。
// 这样做的原因是相册聚合落库与文件下载之间存在时间差，缺失文件时仍应保留文本投递。
func BuildPayloadWithImages(text string, imagePaths []string) Payload {
	payload := Payload{Text: StrUtils.UnescapeHashtags(text)}
	for _, imagePath := range imagePaths {
		if imagePath == "" {
			continue
		}
		if _, err := os.Stat(imagePath); err != nil {
			continue
		}
		payload.Images = append(payload.Images, ImagePayload{FilePath: imagePath})
	}
	return payload
}

func ResolvePayloadImagePath(config Entity.Config, imagePath string) string {
	if imagePath == "" {
		return ""
	}

	if _, err := os.Stat(imagePath); err == nil {
		return imagePath
	}

	if filepath.IsAbs(imagePath) {
		return ""
	}

	candidates := []string{
		filepath.Join(config.Output.ChannelDir, imagePath),
		filepath.Join(config.Output.PersonDir, imagePath),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	return ""
}

// ShouldSync 根据配置和来源ID判定当前消息是否应进入社媒同步。
// 这样做的原因是将策略判断集中管理，确保“配置驱动”规则在全链路一致生效。
func ShouldSync(config Entity.Config, sourceID string) (bool, string) {
	if !config.SocialMediaSync.Enable {
		return false, "社媒同步未启用"
	}

	if len(config.SocialMediaSync.TargetChannel) == 0 {
		return false, "社媒同步目标频道为空，跳过同步"
	}

	if !ContainsExactTarget(config.SocialMediaSync.TargetChannel, sourceID) {
		return false, fmt.Sprintf("未命中社媒同步规则，跳过同步: %s", sourceID)
	}

	return true, ""
}

// ContainsExactTarget 执行目标频道精确匹配，不做模糊匹配。
// 这样做的原因是保持规则可预测，避免误同步到非目标频道。
func ContainsExactTarget(targetChannels []string, sourceID string) bool {
	for _, channel := range targetChannels {
		if channel == sourceID {
			return true
		}
	}
	return false
}

// Dispatch 统一调度多个 Sender 并汇总结果，不关心具体平台实现。
// 这样做的原因是解耦“分发编排”和“平台细节”，便于扩展新平台与测试替身注入。
func Dispatch(config Entity.Config, payload Payload, senders []Sender) []DispatchResult {
	results := make([]DispatchResult, 0, len(senders))
	for _, sender := range senders {
		if sender == nil {
			continue
		}

		result := sender.Send(config, payload)
		if result.Platform == "" {
			result.Platform = sender.Name()
		}
		LogUtils.GetLogger().Printf(
			"sync result platform=%s success=%t image_requested=%t used_image=%t\n",
			result.Platform,
			result.Success,
			result.ImageRequested,
			result.UsedImage,
		)
		results = append(results, result)
	}
	return results
}

func PersistDispatchResults(archivedMessageID int64, results []DispatchResult, trigger DispatchTrigger) error {
	if archivedMessageID <= 0 || len(results) == 0 {
		return nil
	}

	resolvedTrigger := Entity.SyncTrigger(trigger)
	if resolvedTrigger == "" {
		resolvedTrigger = Entity.SyncTriggerAutomatic
	}

	for _, result := range results {
		status := Entity.SyncStatusFailed
		if result.Success {
			status = Entity.SyncStatusSucceeded
		}

		record := &Entity.SyncRecord{
			ArchivedMessageID: archivedMessageID,
			Platform:          result.Platform,
			Status:            status,
			RemoteID:          result.RemoteID,
			RemoteURL:         result.RemoteURL,
			ErrorMessage:      result.ErrorMessage,
			Trigger:           resolvedTrigger,
			ImageRequested:    result.ImageRequested,
			UsedImage:         result.UsedImage,
			CreatedTime:       time.Now(),
		}

		if _, err := Database.SaveSyncRecord(record); err != nil {
			return err
		}
	}

	return nil
}
