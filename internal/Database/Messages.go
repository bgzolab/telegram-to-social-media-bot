package Database

import (
	"strings"
	"telegram-message-sync-bot/internal/Entity"
	"time"
)

type SourceMessageCount struct {
	SourceID      string
	ArchivedCount int64
}

// 保存消息及附件
func SaveMessage(msg *Entity.Message) (int64, error) {
	err := DB.Create(msg).Error
	if err != nil {
		return 0, err
	}
	return msg.ID, nil
}

func IsDuplicateMessageError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := err.Error()
	return strings.Contains(errMsg, "UNIQUE constraint failed") || strings.Contains(errMsg, "duplicated key")
}

// ListMessages 返回全部消息（含附件），用于归档补齐与核对。
func ListMessages() ([]Entity.Message, error) {
	var msgs []Entity.Message
	err := DB.Preload("Attachments").Order("id ASC").Find(&msgs).Error
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// 按ID查找消息（含附件）
func GetMessageByID(id int64) (*Entity.Message, error) {
	var msg Entity.Message
	err := DB.Preload("Attachments").First(&msg, id).Error
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

func CountMessages() (int64, error) {
	var count int64
	err := DB.Model(&Entity.Message{}).Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

func CountDistinctSources() (int64, error) {
	var count int64
	err := DB.Model(&Entity.Message{}).Distinct("username").Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

func ListSourceMessageCounts() ([]SourceMessageCount, error) {
	var rows []SourceMessageCount
	err := DB.Model(&Entity.Message{}).
		Select("username AS source_id, COUNT(*) AS archived_count").
		Group("username").
		Order("archived_count DESC, source_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func GetMessageBySource(messageID int64, username string) (*Entity.Message, error) {
	var msg Entity.Message
	err := DB.Preload("Attachments").Where("message_id = ? AND username = ?", messageID, username).First(&msg).Error
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

func ListMessagesBySourceID(sourceID string) ([]Entity.Message, error) {
	var msgs []Entity.Message
	err := DB.Preload("Attachments").Where("username = ?", sourceID).Order("message_date DESC, id DESC").Find(&msgs).Error
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// 按用户查找消息（含附件）
func GetMessagesByUser(userID string, limit int) ([]Entity.Message, error) {
	var msgs []Entity.Message
	err := DB.Preload("Attachments").
		Where("sender_id = ? OR receiver_id = ?", userID, userID).
		Order("timestamp DESC").
		Limit(limit).
		Find(&msgs).Error
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// 更新消息内容
func UpdateMessage(msg *Entity.Message) error {
	return DB.Save(msg).Error
}

// 删除消息及附件
func DeleteMessage(id int64) error {
	// 先删除附件
	DB.Where("message_id = ?", id).Delete(&Entity.Attachment{})
	return DB.Delete(&Entity.Message{}, id).Error
}

// ListMessagesByMediaGroup 按来源与相册分组ID查询相册全部成员（含附件），按源消息ID升序返回。
// 这样做的原因是相册成员的乱序送达无法通过时间或自增主键推断顺序，只能用源消息ID排序。
func ListMessagesByMediaGroup(sourceID string, mediaGroupID string) ([]Entity.Message, error) {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(mediaGroupID) == "" {
		return nil, nil
	}

	var msgs []Entity.Message
	err := DB.Preload("Attachments").
		Where("username = ? AND media_group_id = ?", sourceID, mediaGroupID).
		Order("message_id ASC, id ASC").
		Find(&msgs).Error
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// HasSuccessfulSyncRecordForMediaGroup 判断相册分组是否已成功完成过自动投递。
// 只统计自动投递且成功的记录：失败记录需要允许重试，手动重同步记录不参与自动路径去重。
func HasSuccessfulSyncRecordForMediaGroup(sourceID string, mediaGroupID string) (bool, error) {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(mediaGroupID) == "" {
		return false, nil
	}

	var count int64
	err := DB.Table("sync_records").
		Joins("JOIN messages ON messages.id = sync_records.archived_message_id").
		Where("messages.username = ? AND messages.media_group_id = ?", sourceID, mediaGroupID).
		Where("sync_records.status = ? AND sync_records.trigger = ?", Entity.SyncStatusSucceeded, Entity.SyncTriggerAutomatic).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// PendingMediaGroup 表示一个已归档但还没有同步记录的相册分组。
type PendingMediaGroup struct {
	Username     string
	MediaGroupID string
}

// ListPendingMediaGroups 列出归档时间在 since 之后、且尚无自动投递成功记录的相册分组。
// 这样做的原因是为进程重启后恢复未完成聚合提供数据来源，同时用时间窗口避免历史相册被重新投递；
// 失败记录与手动重同步记录都不算“已投递”，保证失败相册仍能被自动重试。
func ListPendingMediaGroups(since time.Time) ([]PendingMediaGroup, error) {
	var rows []PendingMediaGroup
	err := DB.Model(&Entity.Message{}).
		Select("username, media_group_id").
		Where("media_group_id <> '' AND created_time >= ?", since).
		Where("NOT EXISTS (SELECT 1 FROM sync_records sr JOIN messages mm ON mm.id = sr.archived_message_id WHERE mm.username = messages.username AND mm.media_group_id = messages.media_group_id AND sr.status = ? AND sr.trigger = ?)", Entity.SyncStatusSucceeded, Entity.SyncTriggerAutomatic).
		Group("username, media_group_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// UpdateMessageMediaGroup 为已归档消息回填相册分组ID，仅更新当前为空的行。
func UpdateMessageMediaGroup(sourceID string, messageID int64, mediaGroupID string) (int64, error) {
	if strings.TrimSpace(mediaGroupID) == "" {
		return 0, nil
	}

	result := DB.Model(&Entity.Message{}).
		Where("username = ? AND message_id = ? AND (media_group_id IS NULL OR media_group_id = '')", sourceID, messageID).
		Update("media_group_id", mediaGroupID)
	return result.RowsAffected, result.Error
}
