// Package albumservice 负责把 Telegram 相册（media_group_id）的多个成员消息聚合成一次社媒投递。
//
// 背景：Bot API 会把相册拆成多条独立 update 下发，所有成员共享 message.media_group_id。
// 如果按普通消息逐条同步，一个 N 张图的相册会产生 N 条帖子，且只有 1 条带正文。
// 本服务在归档完成后按 (来源, media_group_id) 登记分组，静默窗口到期后聚合整组投递一次。
package albumservice

import (
	"context"
	"errors"
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
	// 本地归档数据显示组内消息时间差最大 7 秒（中位 1 秒），这里取 15 秒（>2 倍观测上界），
	// 覆盖慢批次投递；窗口内的新成员会顺延，避免相册被截断成两帖。
	DefaultDebounce = 15 * time.Second
	// MaxAlbumSize 是 Telegram 相册成员上限：收满即立即投递，不再等待静默窗口。
	MaxAlbumSize = 10
	// recoveryWindow 限制启动恢复只处理刚刚归档的相册，避免历史消息被重新投递到社媒。
	// 取值覆盖“进程在静默窗口内退出”的场景即可，窗口之外的分组只能通过手动重同步补投。
	recoveryWindow = 30 * time.Minute
	// maxDeliveryAttempts 是单个分组的最大投递尝试次数，超过后放弃并记录日志。
	maxDeliveryAttempts = 3
)

// MessageSender 是相册服务发送通知所需的最小 Telegram 接口，*bot.Bot 已满足。
type MessageSender interface {
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
}

// Member 描述一条已归档的相册成员。
type Member struct {
	SourceID          string // 归档来源（频道 username / 会话ID），与 Entity.Message.Username 一致
	MediaGroupID      string // Telegram 相册分组ID
	ChatID            int64  // 接收消息的会话ID，用于通知回退
	ArchivedMessageID int64  // 归档消息主键，用于统计组内成员数
}

type groupKey struct {
	sourceID     string
	mediaGroupID string
}

type pendingGroup struct {
	key      groupKey
	chatID   int64
	timer    stopper
	members  map[int64]struct{}
	attempts int
	force    bool
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
		debounce: resolveDebounce(config),
		pending:  make(map[groupKey]*pendingGroup),
		now:      time.Now,
		afterFunc: func(delay time.Duration, callback func()) stopper {
			return time.AfterFunc(delay, callback)
		},
		loadMessages: Database.ListMessagesByMediaGroup,
		hasRecords:   Database.HasSuccessfulSyncRecordForMediaGroup,
		loadPending:  Database.ListPendingMediaGroups,
		dispatch: func(config Entity.Config, payload syncservice.Payload) []syncservice.DispatchResult {
			return syncservice.Dispatch(config, payload, syncservice.DefaultSenders())
		},
		persistResults: func(archivedMessageID int64, results []syncservice.DispatchResult) error {
			return syncservice.PersistDispatchResults(archivedMessageID, results, syncservice.DispatchTriggerAutomatic)
		},
	}
}

// resolveDebounce 解析相册静默窗口：配置未设置或小于 1 秒时回退默认值。
func resolveDebounce(config Entity.Config) time.Duration {
	seconds := config.SocialMediaSync.AlbumDebounceSeconds
	if seconds < 1 {
		return DefaultDebounce
	}
	return time.Duration(seconds) * time.Second
}

// Register 登记一条相册成员；静默窗口内出现新成员会顺延投递时间。
// 已成功自动投递过的分组忽略迟到成员（只归档不重发），避免重复发帖。
func (s *Service) Register(member Member) {
	if s == nil || member.MediaGroupID == "" || member.SourceID == "" {
		return
	}

	synced, err := s.hasRecords(member.SourceID, member.MediaGroupID)
	if err != nil {
		// 读取失败不阻断登记：投递前还会再次预检，最坏情况是重复投递而不是静默丢弃。
		LogUtils.GetLogger().Printf("相册同步状态检查失败: %v\n", err)
	}
	if err == nil && synced {
		LogUtils.GetLogger().Printf("相册分组已投递，跳过迟到成员: source=%s gid=%s\n", member.SourceID, member.MediaGroupID)
		return
	}

	key := groupKey{sourceID: member.SourceID, mediaGroupID: member.MediaGroupID}
	s.schedule(key, member.ChatID, s.debounce, member.ArchivedMessageID)
}

// RecoverPending 在进程启动时恢复最近归档但未完成自动投递的相册分组。
// 这样做的原因是静默窗口定时器只存在于内存，重启会丢失仍未投递的分组；
// 仍在窗口内的分组按剩余窗口调度，而不是直接跳过。
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
		if err != nil {
			LogUtils.GetLogger().Printf("相册恢复加载失败: source=%s gid=%s err=%v\n", group.Username, group.MediaGroupID, err)
			continue
		}
		if len(messages) == 0 {
			continue
		}

		key := groupKey{sourceID: group.Username, mediaGroupID: group.MediaGroupID}
		delay := s.debounce - now.Sub(latestCreatedTime(messages))
		if delay < 0 {
			delay = 0
		}
		s.schedule(key, 0, delay, 0)
	}
}

// schedule 注册/刷新分组的定时器。同一分组重复注册会停止旧定时器并沿用已记录的成员集合。
func (s *Service) schedule(key groupKey, chatID int64, delay time.Duration, archivedMessageID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	members := make(map[int64]struct{}, MaxAlbumSize)
	if existing, ok := s.pending[key]; ok {
		existing.timer.Stop()
		members = existing.members
	}
	if archivedMessageID > 0 {
		members[archivedMessageID] = struct{}{}
	}

	// Telegram 相册上限为 10 条：收满立即投递，不必再等静默窗口。
	force := len(members) >= MaxAlbumSize
	if force {
		delay = 0
	}

	s.replaceLocked(key, chatID, delay, members, 0, force)
}

// retry 在投递异常时按静默窗口重排定时器，超过尝试上限后放弃；已有更新的登记排队时不重复重排。
func (s *Service) retry(group *pendingGroup, cause error) {
	attempts := group.attempts + 1
	if attempts >= maxDeliveryAttempts {
		LogUtils.GetLogger().Printf("相册投递重试达到上限，放弃: source=%s gid=%s err=%v\n", group.key.sourceID, group.key.mediaGroupID, cause)
		return
	}

	LogUtils.GetLogger().Printf("相册投递失败，等待重试: source=%s gid=%s err=%v\n", group.key.sourceID, group.key.mediaGroupID, cause)
	s.replaceIfIdle(group, s.debounce, attempts)
}

// replaceIfIdle 仅在分组当前没有更新登记排队时重排定时器，避免覆盖并发的 Register 结果。
func (s *Service) replaceIfIdle(group *pendingGroup, delay time.Duration, attempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.pending[group.key]; exists {
		return
	}
	s.replaceLocked(group.key, group.chatID, delay, group.members, attempts, group.force)
}

// replaceLocked 持有 s.mu 时替换分组定时器。
func (s *Service) replaceLocked(key groupKey, chatID int64, delay time.Duration, members map[int64]struct{}, attempts int, force bool) {
	group := &pendingGroup{key: key, chatID: chatID, members: members, attempts: attempts, force: force}
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
	messages, err := s.loadMessages(group.key.sourceID, group.key.mediaGroupID)
	if err != nil || len(messages) == 0 {
		s.retry(group, err)
		return
	}

	// 窗口内可能又有成员到达：顺延到剩余窗口再投递，避免把相册截断成两帖。
	// 收满上限的分组已经完整，不受窗口检查影响。
	if !group.force {
		if remaining := s.debounce - s.now().Sub(latestCreatedTime(messages)); remaining > 0 {
			s.replaceIfIdle(group, remaining, group.attempts)
			return
		}
	}

	// 与手动重同步共享相册维度防重入，避免聚合投递与手动重同步并发重复发帖。
	scope := syncservice.AlbumScopeKey(group.key.sourceID, group.key.mediaGroupID, 0)
	if !syncservice.AcquireAlbumDispatch(scope) {
		s.retry(group, nil)
		return
	}
	defer syncservice.ReleaseAlbumDispatch(scope)

	// 成功投递预检放在取得互斥之后，关闭“预检通过后手动重同步完成”的竞态窗口。
	if synced, err := s.hasRecords(group.key.sourceID, group.key.mediaGroupID); err == nil && synced {
		return
	}

	head := syncservice.AlbumHead(messages)
	payload := syncservice.BuildPayloadWithImages(syncservice.AlbumText(messages), syncservice.CollectAlbumImagePaths(s.config, messages))
	results := s.dispatch(s.config, payload)
	if len(results) == 0 {
		s.retry(group, errors.New("dispatch returned no results"))
		return
	}

	for _, message := range messages {
		if err := s.persistResults(message.ID, results); err != nil {
			LogUtils.GetLogger().Printf("相册同步记录写入失败: %v\n", err)
		}
	}

	s.notify(group.chatID, head.MessageUrl, len(payload.ImagePaths()), results)

	// 全部平台失败时按上限重试；失败记录不参与“已成功投递”预检，重启恢复也能兜底。
	if allResultsFailed(results) {
		s.retry(group, errors.New("all platforms failed"))
	}
}

func allResultsFailed(results []syncservice.DispatchResult) bool {
	for _, result := range results {
		if result.Success {
			return false
		}
	}
	return true
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
			LogUtils.GetLogger().Printf("相册同步通知缺少目标会话，已跳过: sourceLink=%s\n", sourceLink)
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
