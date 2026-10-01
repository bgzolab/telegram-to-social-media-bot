package jsonbackfillservice

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-telegram/bot/models"

	"telegram-message-sync-bot/internal/Database"
	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/internal/service/archiveservice"
)

// BackfillStats 汇总历史 JSON 补录数据库的处理结果。
// 该结构当前只承载统计值与失败文件列表，不包含原始 JSON 内容。
type BackfillStats struct {
	Scanned     int
	Decoded     int
	Inserted    int
	Duplicates  int
	Skipped     int
	Failed      int
	FailedFiles []string
}

// BackfillFromJSON 是历史 JSON 补录数据库的唯一服务入口。
// 当前按确定性顺序扫描、解码并标准化消息，后续任务再补齐 archiveTime、入库和错误聚合。
func BackfillFromJSON(config Entity.Config) (BackfillStats, error) {
	stats := BackfillStats{}

	paths, err := collectJSONFiles(config.Output.JsonDir)
	if err != nil {
		return stats, err
	}

	for _, path := range paths {
		stats.Scanned++

		update, info, err := decodeUpdate(path)
		if err != nil {
			stats.Failed++
			stats.FailedFiles = append(stats.FailedFiles, path)
			continue
		}

		stats.Decoded++
		if update.Message == nil {
			stats.Skipped++
			continue
		}

		meta := archiveservice.ResolveSourceMeta(update, config)
		msgText := archiveservice.SelectMsgText(update)
		archiveTime := meta.SourceDate
		if info != nil {
			archiveTime = info.ModTime()
		}

		message := archiveservice.BuildArchivedMessage(meta, msgText, archiveTime, nil, update.Message.MediaGroupID)
		if _, err := Database.SaveMessage(&message); err != nil {
			if Database.IsDuplicateMessageError(err) {
				stats.Duplicates++
				continue
			}

			stats.Failed++
			stats.FailedFiles = append(stats.FailedFiles, path)
			continue
		}

		stats.Inserted++
	}

	if stats.Failed > 0 {
		return stats, summarizeBackfillFailure(stats)
	}

	return stats, nil
}

func summarizeBackfillFailure(stats BackfillStats) error {
	const maxExamples = 5

	files := stats.FailedFiles
	if len(files) > maxExamples {
		files = files[:maxExamples]
	}

	return fmt.Errorf("json backfill completed with %d failed files: %s", stats.Failed, strings.Join(files, ", "))
}

func collectJSONFiles(root string) ([]string, error) {
	files := make([]string, 0)

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".json") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(files)
	return files, nil
}

func decodeUpdate(path string) (*models.Update, os.FileInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	var update models.Update
	if err := json.Unmarshal(data, &update); err != nil {
		return nil, nil, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return &update, nil, nil
	}

	return &update, info, nil
}

// MediaGroupBackfillStats 汇总历史相册分组ID的回填结果。
type MediaGroupBackfillStats struct {
	Scanned     int
	WithGroup   int
	Updated     int
	Unchanged   int
	Skipped     int
	Failed      int
	FailedFiles []string
}

// BackfillMediaGroupIDs 扫描历史 JSON，把 media_group_id 回填到已归档消息行。
// 这样做的原因是功能上线前入库的消息没有分组ID，只能通过原始 JSON 一次性补齐才能识别历史相册。
// 回填只更新空分组ID，不修改正文、附件与同步状态，也不会触发任何社媒投递。
func BackfillMediaGroupIDs(config Entity.Config) (MediaGroupBackfillStats, error) {
	stats := MediaGroupBackfillStats{}

	paths, err := collectJSONFiles(config.Output.JsonDir)
	if err != nil {
		return stats, err
	}

	for _, path := range paths {
		stats.Scanned++

		update, _, err := decodeUpdate(path)
		if err != nil {
			stats.Failed++
			stats.FailedFiles = append(stats.FailedFiles, path)
			continue
		}
		if update.Message == nil || strings.TrimSpace(update.Message.MediaGroupID) == "" {
			stats.Skipped++
			continue
		}

		stats.WithGroup++
		meta := archiveservice.ResolveSourceMeta(update, config)
		affected, err := Database.UpdateMessageMediaGroup(meta.SourceID, int64(meta.MessageID), update.Message.MediaGroupID)
		if err != nil {
			stats.Failed++
			stats.FailedFiles = append(stats.FailedFiles, path)
			continue
		}
		if affected > 0 {
			stats.Updated++
			continue
		}
		stats.Unchanged++
	}

	if stats.Failed > 0 {
		return stats, summarizeMediaGroupBackfillFailure(stats)
	}

	return stats, nil
}

func summarizeMediaGroupBackfillFailure(stats MediaGroupBackfillStats) error {
	const maxExamples = 5

	files := stats.FailedFiles
	if len(files) > maxExamples {
		files = files[:maxExamples]
	}

	return fmt.Errorf("media group backfill completed with %d failed files: %s", stats.Failed, strings.Join(files, ", "))
}
