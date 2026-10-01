package syncservice

import (
	"fmt"
	"strings"
	"sync"

	"telegram-message-sync-bot/internal/Database"
	"telegram-message-sync-bot/internal/Entity"
)

type ManualResyncResult struct {
	Requested bool
	Reason    string
	Results   []DispatchResult
}

var manualDispatchFactory = DefaultSenders
var manualDispatchGuard = newInFlightGuard()

type inFlightGuard struct {
	mu       sync.Mutex
	inFlight map[string]struct{}
}

func newInFlightGuard() *inFlightGuard {
	return &inFlightGuard{inFlight: make(map[string]struct{})}
}

func (g *inFlightGuard) Acquire(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.inFlight[key]; exists {
		return false
	}
	g.inFlight[key] = struct{}{}
	return true
}

func (g *inFlightGuard) Release(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.inFlight, key)
}

func ManualResync(config Entity.Config, archivedMessageID int64, platform string) (ManualResyncResult, error) {
	msg, err := Database.GetMessageByID(archivedMessageID)
	if err != nil {
		return ManualResyncResult{}, err
	}

	if !ContainsExactTarget(config.SocialMediaSync.TargetChannel, msg.Username) {
		return ManualResyncResult{Requested: false, Reason: "该消息来源不在可同步频道内"}, nil
	}

	senders, err := resolveManualSenders(platform)
	if err != nil {
		return ManualResyncResult{}, err
	}

	// 相册成员共享 (来源, 分组ID) 作用域，避免不同成员各点一次重同步、或与聚合投递并发导致重复发帖；
	// 普通消息仍按“归档消息 + 平台”粒度控制。
	guardKey := AlbumScopeKey(msg.Username, msg.MediaGroupID, msg.ID)
	if msg.MediaGroupID == "" {
		guardKey = fmt.Sprintf("%s:%s", guardKey, NormalizePlatform(platform))
	}
	if !manualDispatchGuard.Acquire(guardKey) {
		return ManualResyncResult{Requested: false, Reason: "相同范围的重同步正在执行，请稍后再试"}, nil
	}
	defer manualDispatchGuard.Release(guardKey)

	messages := resyncAlbumMessages(msg)
	if len(messages) == 0 {
		return ManualResyncResult{}, fmt.Errorf("消息不存在或已删除: %d", archivedMessageID)
	}

	payload := BuildPayloadWithImages(AlbumText(messages), CollectAlbumImagePaths(config, messages))
	results := Dispatch(config, payload, senders)
	if err := PersistDispatchResults(archivedMessageID, results, DispatchTriggerManual); err != nil {
		return ManualResyncResult{}, err
	}

	return ManualResyncResult{Requested: true, Results: results}, nil
}

// AlbumScopeKey 返回相册/消息投递防重入的作用域标识。
// 相册按 (来源, 分组ID)，普通消息按归档消息ID；这样同一相册的不同成员共享一个作用域。
func AlbumScopeKey(sourceID string, mediaGroupID string, archivedMessageID int64) string {
	if mediaGroupID != "" {
		return fmt.Sprintf("album:%s|%s", sourceID, mediaGroupID)
	}
	return fmt.Sprintf("message:%d", archivedMessageID)
}

// AcquireAlbumDispatch 获取相册维度的投递令牌；同一相册已被投递时返回 false。
// 这样做的原因是让手动重同步与相册聚合投递共享同一把防重入锁。
func AcquireAlbumDispatch(scope string) bool {
	return manualDispatchGuard.Acquire(scope)
}

// ReleaseAlbumDispatch 释放相册维度的投递令牌。
func ReleaseAlbumDispatch(scope string) {
	manualDispatchGuard.Release(scope)
}

// resyncAlbumMessages 返回手动重同步实际要处理的成员集合：相册成员聚合整组，普通消息只处理自身。
// 这样做的原因是相册成员单独重同步会产生只含一张图的残缺帖子。
func resyncAlbumMessages(msg *Entity.Message) []Entity.Message {
	if msg == nil {
		return nil
	}
	if msg.MediaGroupID == "" {
		return []Entity.Message{*msg}
	}

	group, err := Database.ListMessagesByMediaGroup(msg.Username, msg.MediaGroupID)
	if err != nil || len(group) == 0 {
		return []Entity.Message{*msg}
	}
	return group
}

func resolveManualSenders(platform string) ([]Sender, error) {
	if NormalizePlatform(platform) == "all" {
		return manualDispatchFactory(), nil
	}

	selected := make([]Sender, 0, 1)
	for _, sender := range manualDispatchFactory() {
		if NormalizePlatform(sender.Name()) == NormalizePlatform(platform) {
			selected = append(selected, sender)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("未知平台: %s", platform)
	}
	return selected, nil
}

func NormalizePlatform(platform string) string {
	platform = strings.TrimSpace(strings.ToLower(platform))
	switch platform {
	case "bs", "bluesky":
		return "bluesky"
	case "md", "mastodon":
		return "mastodon"
	case "tw", "twitter":
		return "twitter"
	case "all":
		return "all"
	default:
		return platform
	}
}
