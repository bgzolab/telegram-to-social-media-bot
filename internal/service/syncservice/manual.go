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

	guardKey := fmt.Sprintf("%d:%s", archivedMessageID, NormalizePlatform(platform))
	if !manualDispatchGuard.Acquire(guardKey) {
		return ManualResyncResult{Requested: false, Reason: "相同范围的重同步正在执行，请稍后再试"}, nil
	}
	defer manualDispatchGuard.Release(guardKey)

	messages := resyncAlbumMessages(msg)
	payload := BuildPayloadWithImages(AlbumText(messages), CollectAlbumImagePaths(config, messages))
	results := Dispatch(config, payload, senders)
	if err := PersistDispatchResults(archivedMessageID, results, DispatchTriggerManual); err != nil {
		return ManualResyncResult{}, err
	}

	return ManualResyncResult{Requested: true, Results: results}, nil
}

// resyncAlbumMessages 返回手动重同步实际要处理的成员集合：相册成员聚合整组，普通消息只处理自身。
// 这样做的原因是相册成员单独重同步会产生只含一张图的残缺帖子。
func resyncAlbumMessages(msg *Entity.Message) []Entity.Message {
	if msg == nil || msg.MediaGroupID == "" {
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
