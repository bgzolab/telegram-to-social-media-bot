// Package albumservice 负责把 Telegram 相册（media_group_id）的多个成员消息聚合成一次社媒投递。
//
// 背景：Bot API 会把相册拆成多条独立 update 下发，所有成员共享 message.media_group_id。
// 如果按普通消息逐条同步，一个 N 张图的相册会产生 N 条帖子，且只有 1 条带正文。
// 本服务在归档完成后按 (来源, media_group_id) 登记分组，静默窗口到期后聚合整组投递一次。
package albumservice

import (
	"context"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-message-sync-bot/internal/Database"
	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/internal/service/notifyservice"
	"telegram-message-sync-bot/internal/service/syncservice"
	"telegram-message-sync-bot/pkg/LogUtils"
)

const (
	// DefaultDebounce 是相册聚合的静默窗口：最后一条成员到达后超过该时长没有新成员才触发投递。
	// 本地归档数据显示组内消息时间差最大 7 秒（中位 1 秒），5 秒可以覆盖常见投递抖动。
	DefaultDebounce = 5 * time.Second
	// MaxAlbumSize 是 Telegram 相册成员上限；达到上限时立即投递，不再等待静默窗口。
	MaxAlbumSize = 10
	// recoveryWindow 限制启动恢复只处理刚刚归档的相册，避免历史消息被重新投递到社媒。
	// 取值覆盖“进程在静默窗口内退出”的场景即可，窗口之外的分组只能通过手动重同步补投。
	recoveryWindow = 30 * time.Minute
)

// MessageSender 是相册服务发送通知所需的最小 Telegram 接口，*bot.Bot 已满足。
type MessageSender interface {
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
}

// Member 描述一条已归档的相册成员。
type Member struct {
	SourceID     string // 归档来源（频道 username / 会话ID），与 Entity.Message.Username 一致
	MediaGroupID string // Telegram 相册分组ID
	ChatID       int64  // 接收消息的会话ID，用于通知回退
}

type groupKey struct {
	sourceID     string
	mediaGroupID string
}

type pendingGroup struct {
	key    groupKey
	chatID int64
	timer  stopper
}

// stopper 抽象 *time.Timer，便于测试注入可控定时器。
type stopper interface {
	Stop() bool
}

// Service 维护相册分组的静默窗口定时器，并在窗口到期后执行整组投递。
type Service struct {
	sender   MessageSender
	config   Entity.Config
	debounce time.Duration

	mu      sync.Mutex
	pending map[groupKey]*pendingGroup

	now       func() time.Time
	afterFunc func(time.Duration, func()) stopper

	loadMessages   func(sourceID string, mediaGroupID string) ([]Entity.Message, error)
	hasRecords     func(sourceID string, mediaGroupID string) (bool, error)
	loadPending    func(since time.Time) ([]Database.PendingMediaGroup, error)
	dispatch       func(config Entity.Config, payload syncservice.Payload) []syncservice.DispatchResult
	persistResults func(archivedMessageID int64, results []syncservice.DispatchResult) error
}

// New 构造相册聚合服务。sender 可为 nil（例如离线迁移场景），此时只投递不发送通知。
func New(sender MessageSender, config Entity.Config) *Service {
	return &Service{
		sender:   sender,
		config:   config,
		debounce: DefaultDebounce,
		pending:  make(map[groupKey]*pendingGroup),
		now:      time.Now,
		afterFunc: func(delay time.Duration, callback func()) stopper {
			return time.AfterFunc(delay, callback)
		},
		loadMessages: Database.ListMessagesByMediaGroup,
		hasRecords:   Database.HasSyncRecordsForMediaGroup,
		loadPending:  Database.ListPendingMediaGroups,
		dispatch: func(config Entity.Config, payload syncservice.Payload) []syncservice.DispatchResult {
			return syncservice.Dispatch(config, payload, syncservice.DefaultSenders())
		},
		persistResults: func(archivedMessageID int64, results []syncservice.DispatchResult) error {
			return syncservice.PersistDispatchResults(archivedMessageID, results, syncservice.DispatchTriggerAutomatic)
		},
	}
}

// Register 登记一条相册成员；静默窗口内出现新成员会顺延投递时间。
// 已产生过同步记录的分组直接忽略，避免迟到的成员触发第二次相册投递。
func (s *Service) Register(member Member) {
	if s == nil || member.MediaGroupID == "" || member.SourceID == "" {
		return
	}

	synced, err := s.hasRecords(member.SourceID, member.MediaGroupID)
	if err != nil {
		LogUtils.GetLogger().Printf("相册同步状态检查失败: %v\n", err)
		return
	}
	if synced {
		return
	}

	key := groupKey{sourceID: member.SourceID, mediaGroupID: member.MediaGroupID}
	s.schedule(key, member.ChatID, s.debounce)
}

// RecoverPending 在进程启动时恢复最近归档但未完成投递的相册分组。
// 这样做的原因是静默窗口定时器只存在于内存，重启会丢失仍未投递的分组。
func (s *Service) RecoverPending() {
	if s == nil {
		return
	}

	groups, err := s.loadPending(s.now().Add(-recoveryWindow))
	if err != nil {
		LogUtils.GetLogger().Printf("相册恢复扫描失败: %v\n", err)
		return
	}

	now := s.now()
	for _, group := range groups {
		if enabled, _ := syncservice.ShouldSync(s.config, group.Username); !enabled {
			continue
		}

		messages, err := s.loadMessages(group.Username, group.MediaGroupID)
		if err != nil || len(messages) == 0 {
			continue
		}
		if now.Sub(latestCreatedTime(messages)) < s.debounce {
			// 疑似仍在收集中，交给正常注册路径处理。
			continue
		}

		key := groupKey{sourceID: group.Username, mediaGroupID: group.MediaGroupID}
		s.schedule(key, 0, 0)
	}
}

// schedule 注册/刷新分组的定时器。同一分组重复注册会停止旧定时器，只保留最后一次。
func (s *Service) schedule(key groupKey, chatID int64, delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.pending[key]; ok {
		existing.timer.Stop()
	}

	group := &pendingGroup{key: key, chatID: chatID}
	group.timer = s.afterFunc(delay, func() {
		s.flush(group)
	})
	s.pending[key] = group
}

// flush 触发分组投递；若分组已被更新的登记替换或已移除，则直接跳过，避免重复投递。
func (s *Service) flush(group *pendingGroup) {
	s.mu.Lock()
	current, ok := s.pending[group.key]
	if !ok || current != group {
		s.mu.Unlock()
		return
	}
	delete(s.pending, group.key)
	s.mu.Unlock()

	s.deliver(group)
}

// deliver 读取落库的相册成员，聚合文本与图片后统一投递，并把结果写入每个成员的同步记录。
func (s *Service) deliver(group *pendingGroup) {
	// 静默窗口期间可能已发生手动重同步，投递前再确认一次，避免重复发帖。
	if synced, err := s.hasRecords(group.key.sourceID, group.key.mediaGroupID); err == nil && synced {
		return
	}

	messages, err := s.loadMessages(group.key.sourceID, group.key.mediaGroupID)
	if err != nil {
		LogUtils.GetLogger().Printf("相册成员加载失败: %v\n", err)
		return
	}
	if len(messages) == 0 {
		return
	}

	head := syncservice.AlbumHead(messages)
	payload := syncservice.BuildPayloadWithImages(syncservice.AlbumText(messages), syncservice.CollectAlbumImagePaths(s.config, messages))
	results := s.dispatch(s.config, payload)
	if len(results) == 0 {
		return
	}

	for _, message := range messages {
		if err := s.persistResults(message.ID, results); err != nil {
			LogUtils.GetLogger().Printf("相册同步记录写入失败: %v\n", err)
		}
	}

	s.notify(group.chatID, head.MessageUrl, len(payload.ImagePaths()), results)
}

func (s *Service) notify(chatID int64, sourceLink string, imageCount int, results []syncservice.DispatchResult) {
	if s.sender == nil {
		return
	}

	texts := notifyservice.BuildAlbumSyncNotifications(sourceLink, imageCount, results)
	if len(texts) == 0 {
		return
	}

	for _, target := range notifyservice.ResolveTargetChatIDs(s.config, chatID) {
		if target == 0 {
			continue
		}
		for _, text := range texts {
			if _, err := s.sender.SendMessage(context.Background(), &bot.SendMessageParams{ChatID: target, Text: text}); err != nil {
				LogUtils.GetLogger().Printf("相册同步通知发送失败: %v\n", err)
			}
		}
	}
}

func latestCreatedTime(messages []Entity.Message) time.Time {
	latest := time.Time{}
	for _, message := range messages {
		if message.CreatedTime.After(latest) {
			latest = message.CreatedTime
		}
	}
	return latest
}
