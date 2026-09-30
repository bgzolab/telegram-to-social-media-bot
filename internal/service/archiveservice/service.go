package archiveservice

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"telegram-message-sync-bot/internal/Database"
	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/pkg/FileUtils"
	"telegram-message-sync-bot/pkg/LogUtils"
	"telegram-message-sync-bot/pkg/StrUtils"
	"telegram-message-sync-bot/pkg/TgUtils"
	"time"
	"unicode"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type PersistResult struct {
	OK                bool
	Message           string
	SourceLink        string
	MsgText           string
	SourceID          string
	ImagePath         string
	ArchivedMessageID int64
	MediaGroupID      string // Telegram 相册分组ID，非相册消息为空
	ChatID            int64  // 消息所在会话，供异步通知回退使用
}

// downloadFile 下载 Telegram 文件内容；测试可替换为 fake 以离线运行。
var downloadFile = func(url string) (*http.Response, error) {
	return http.Get(url)
}

type SourceMeta struct {
	OutputPath  string
	ArchiveRoot string
	SourceID    string
	FileName    string
	SourceLink  string
	SourceDate  time.Time
	MessageID   int
}

// PersistMessage 负责执行“单条消息归档”完整编排：解析来源、渲染模板、落盘、入库。
// 这样做的原因是把归档用例从入口层抽离，降低 main 复杂度，并让归档逻辑可独立测试与复用。
func PersistMessage(ctx context.Context, b *bot.Bot, update *models.Update, config Entity.Config) PersistResult {
	if update.Message == nil {
		return PersistResult{OK: false, Message: "接受消息为空"}
	}

	meta := ResolveSourceMeta(update, config)
	msgText := SelectMsgText(update)
	mediaGroupID := strings.TrimSpace(update.Message.MediaGroupID)
	chatID := update.Message.Chat.ID
	meta.FileName = ResolveArchiveFileName(config, meta, msgText)
	photoLink := ""
	imagePath := ""
	assets := []Entity.Attachment{}
	existingArchivedMessageID := resolveArchivedMessageID(int64(meta.MessageID), meta.SourceID)

	if meta.SourceLink != "" && isMessageArchived(meta) {
		return PersistResult{OK: false, Message: "消息已存在", SourceLink: meta.SourceLink, MsgText: msgText, SourceID: meta.SourceID, ArchivedMessageID: existingArchivedMessageID, MediaGroupID: mediaGroupID, ChatID: chatID}
	}

	if photos := extractPhotos(update); len(photos) > 0 {
		var files []string
		highestResolutionPhoto := photos[len(photos)-1]
		file := persistFile(ctx, b, highestResolutionPhoto.FileID, highestResolutionPhoto.FileUniqueID, meta.SourceID, meta.ArchiveRoot, Entity.ImageMessage)
		if file != nil {
			if config.R2.Enable && file.Type == Entity.ImageMessage {
				url, err := uploadAttachmentFactory(ctx, config, meta, file)
				if err != nil {
					LogUtils.GetLogger().Printf("r2 upload failed: %v\n", err)
				} else {
					file.S3Url = url
				}
			}
			files = append(files, PreferredAttachmentURL(*file))
			assets = append(assets, *file)
			imagePath = file.FilePath
		}
		photoLink = formatDownloadedFiles(files)
	}

	logCommandline := fmt.Sprintf("ChatID: %d, Channel: %s, Message: %s",
		update.Message.Chat.ID,
		meta.SourceID,
		msgText,
	)
	LogUtils.GetLogger().Println(logCommandline)

	archiveNow := time.Now()
	data := BuildTemplateData(meta, photoLink, msgText, archiveNow)

	tmplData, err := os.ReadFile(config.Template.Dir)
	if err != nil {
		return PersistResult{OK: false, Message: fmt.Sprintf("读取模板失败, %v", err), SourceLink: meta.SourceLink, MsgText: msgText, SourceID: meta.SourceID, ImagePath: imagePath, ArchivedMessageID: existingArchivedMessageID, MediaGroupID: mediaGroupID, ChatID: chatID}
	}

	tmpl, err := template.New("example").Parse(string(tmplData))
	if err != nil {
		return PersistResult{OK: false, Message: fmt.Sprintf("解析模板失败, %v", err), SourceLink: meta.SourceLink, MsgText: msgText, SourceID: meta.SourceID, ImagePath: imagePath, ArchivedMessageID: existingArchivedMessageID, MediaGroupID: mediaGroupID, ChatID: chatID}
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return PersistResult{OK: false, Message: fmt.Sprintf("渲染模板失败, %v", err), SourceLink: meta.SourceLink, MsgText: msgText, SourceID: meta.SourceID, ImagePath: imagePath, ArchivedMessageID: existingArchivedMessageID, MediaGroupID: mediaGroupID, ChatID: chatID}
	}
	contentWithFrontMatter := strings.TrimLeft(buf.String(), "\n")

	FileUtils.OutputString(meta.OutputPath, meta.FileName, contentWithFrontMatter)

	savedMsg := BuildArchivedMessage(meta, msgText, archiveNow, assets, mediaGroupID)

	messageID, err := Database.SaveMessage(&savedMsg)
	if err != nil {
		if Database.IsDuplicateMessageError(err) {
			return PersistResult{OK: false, Message: "消息已存在", SourceLink: meta.SourceLink, MsgText: msgText, SourceID: meta.SourceID, ImagePath: imagePath, ArchivedMessageID: resolveArchivedMessageID(int64(meta.MessageID), meta.SourceID), MediaGroupID: mediaGroupID, ChatID: chatID}
		}

		LogUtils.GetLogger().Println(err)
		return PersistResult{OK: false, Message: fmt.Sprintf("消息入库失败: %v", err), SourceLink: meta.SourceLink, MsgText: msgText, SourceID: meta.SourceID, ImagePath: imagePath, ArchivedMessageID: existingArchivedMessageID, MediaGroupID: mediaGroupID, ChatID: chatID}
	}

	LogUtils.GetLogger().Printf("Save successful with: %d\n", messageID)
	return PersistResult{OK: true, Message: meta.FileName, SourceLink: meta.SourceLink, MsgText: msgText, SourceID: meta.SourceID, ImagePath: imagePath, ArchivedMessageID: messageID, MediaGroupID: mediaGroupID, ChatID: chatID}
}

// BuildArchivedMessage 统一构造数据库消息对象，供实时归档和历史补录共同复用。
// 这样做的原因是把消息字段映射规则固定在单点，避免不同入口写出不一致的数据库记录。
func BuildArchivedMessage(meta SourceMeta, msgText string, archiveTime time.Time, assets []Entity.Attachment, mediaGroupID string) Entity.Message {
	return Entity.Message{
		Content: msgText,

		MessageID:    int64(meta.MessageID),
		Username:     meta.SourceID,
		MessageUrl:   meta.SourceLink,
		MessageDate:  meta.SourceDate,
		MediaGroupID: strings.TrimSpace(mediaGroupID),
		Attachments:  assets,

		CreatedTime: archiveTime,
	}
}

func resolveArchivedMessageID(messageID int64, sourceID string) int64 {
	if messageID == 0 || sourceID == "" {
		return 0
	}

	msg, err := Database.GetMessageBySource(messageID, sourceID)
	if err != nil {
		return 0
	}
	return msg.ID
}

// BuildFrontMatter 生成强制 Front Matter。
// 规则：
// 1) title/aliases/description 基于正文摘要；
// 2) 摘要计算前先把所有 \n 替换为空格；
// 3) 时间格式固定为 yyyy-MM-ddTHH:mm:ss（不带时区后缀）。
func BuildFrontMatter(meta SourceMeta, content string, archivedAt time.Time) string {
	normalized := normalizeFrontMatterContent(content)
	titleSummary := truncateForFrontMatter(normalized, 50)
	descSummary := truncateForFrontMatter(normalized, 100)

	title := fmt.Sprintf("%d-%s", meta.MessageID, titleSummary)
	source := meta.SourceLink
	created := formatFrontMatterTime(meta.SourceDate)
	modified := formatFrontMatterTime(archivedAt)

	return strings.Join([]string{
		"---",
		fmt.Sprintf("title: %s", quoteYAMLString(title)),
		"aliases:",
		fmt.Sprintf("- %s", quoteYAMLString(title)),
		fmt.Sprintf("created: %s", created),
		fmt.Sprintf("modified: %s", modified),
		"comments: true",
		"draft: true",
		fmt.Sprintf("description: %s", quoteYAMLString(descSummary)),
		fmt.Sprintf("source: %s", quoteYAMLString(source)),
		"tags: []",
		"---",
	}, "\n")
}

func normalizeFrontMatterContent(content string) string {
	replaced := strings.ReplaceAll(content, "\r\n", "\n")
	replaced = strings.ReplaceAll(replaced, "\r", "\n")
	return strings.ReplaceAll(replaced, "\n", " ")
}

func truncateForFrontMatter(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes])
}

func formatFrontMatterTime(t time.Time) string {
	return t.Format("2006-01-02T15:04:05")
}

func quoteYAMLString(s string) string {
	return strconv.Quote(s)
}

// isMessageArchived 切换期兼容读取：新格式按目标文件是否存在判断，旧格式仍回退到单文件内容搜索。
func isMessageArchived(meta SourceMeta) bool {
	newArchivePath := filepath.Join(meta.OutputPath, meta.FileName)
	if info, err := os.Stat(newArchivePath); err == nil && !info.IsDir() {
		return true
	}

	if meta.SourceLink == "" {
		return false
	}

	legacyArchivePath := filepath.Join(meta.ArchiveRoot, fmt.Sprintf("%s.md", meta.SourceID))
	return StrUtils.SearchInFile(legacyArchivePath, meta.SourceLink)
}

// ResolveSourceMeta 将 Telegram 原始消息转换为统一来源元信息（来源ID、文件名、落盘路径、消息链接等）。
// 这样做的原因是统一“私聊消息/频道转发消息”的分支处理，避免上层重复判断来源类型。
func ResolveSourceMeta(update *models.Update, config Entity.Config) SourceMeta {
	messageDate := time.Now()
	if update != nil && update.Message != nil {
		messageDate = time.Unix(int64(update.Message.Date), 0)
	}

	meta := SourceMeta{
		ArchiveRoot: config.Output.PersonDir,
		SourceID:    fmt.Sprintf("%d", update.Message.Chat.ID),
		SourceDate:  messageDate,
		MessageID:   update.Message.ID,
	}

	if update.Message.ForwardOrigin != nil && update.Message.ForwardOrigin.Type == "channel" {
		meta.ArchiveRoot = config.Output.ChannelDir
		origin := update.Message.ForwardOrigin.MessageOriginChannel

		if origin.Chat.Username != "" {
			meta.SourceID = origin.Chat.Username
		} else {
			meta.SourceID = fmt.Sprintf("%d", origin.Chat.ID)
		}

		meta.SourceLink = fmt.Sprintf("https://t.me/%s/%d", meta.SourceID, origin.MessageID)
		meta.SourceDate = time.Unix(int64(origin.Date), 0)
		meta.MessageID = origin.MessageID
	}

	// 步骤1：仅切换归档写入路径到 source_id/message_id.md。
	meta.OutputPath = filepath.Join(meta.ArchiveRoot, meta.SourceID)
	meta.FileName = fmt.Sprintf("%d.md", meta.MessageID)

	return meta
}

// SelectMsgText 统一提取消息正文：优先 Text，回退 Caption，并处理标签转义和文本链接格式化。
// 这样做的原因是把文本处理规则集中，保证归档内容在不同消息类型下行为一致。
func SelectMsgText(update *models.Update) string {
	msgText := update.Message.Text
	msgEntities := update.Message.Entities
	if msgText == "" {
		msgText = update.Message.Caption
		msgEntities = update.Message.CaptionEntities
	}
	return StrUtils.EscapeHashtags(TgUtils.HandleMsgLink(msgText, msgEntities))
}

func extractPhotos(update *models.Update) []models.PhotoSize {
	if update == nil || update.Message == nil {
		return nil
	}

	return update.Message.Photo
}

// BuildTemplateData 生成归档模板渲染字段，统一标题规则、时间格式和兼容别名。
// 这样做的原因是让完整 Markdown 模板成为唯一输出边界，同时降低模板升级成本。
func BuildTemplateData(meta SourceMeta, photoLink, msgText string, now time.Time) map[string]interface{} {
	published := formatFrontMatterTime(meta.SourceDate)
	saved := formatFrontMatterTime(now)
	title := buildArchiveTitle(msgText, meta.MessageID)

	return map[string]interface{}{
		"id":             meta.MessageID,
		"title":          title,
		"published":      published,
		"modified":       saved,
		"saved":          saved,
		"source":         meta.SourceLink,
		"source_channel": meta.SourceID,
		"photo":          photoLink,
		"content":        msgText,

		// 兼容旧模板占位符，避免升级代码后现有模板立即失效。
		"source_message": meta.SourceLink,
		"sourceTelegram": meta.SourceLink,
		"date":           published,
		"now":            saved,
	}
}

func buildArchiveTitle(content string, messageID int) string {
	normalized := strings.TrimSpace(normalizeFrontMatterContent(content))
	title := truncateForFrontMatter(normalized, 20)
	if title != "" {
		return title
	}
	return strconv.Itoa(messageID)
}

// ResolveArchiveFileName 按配置模板生成归档文件名；未配置时保持旧规则 message_id.md。
// 这样做的原因是把文件名策略放在拥有正文上下文的一层，确保 title 规则可用且稳定。
func ResolveArchiveFileName(config Entity.Config, meta SourceMeta, content string) string {
	tmpl := strings.TrimSpace(config.Template.FileNameTemplate)
	if tmpl == "" {
		return fmt.Sprintf("%d.md", meta.MessageID)
	}

	title := sanitizeArchiveFileNameTitle(buildArchiveTitle(content, meta.MessageID))
	if title == "" {
		title = strconv.Itoa(meta.MessageID)
	}

	rendered := tmpl
	rendered = strings.ReplaceAll(rendered, "{{.id}}", strconv.Itoa(meta.MessageID))
	rendered = strings.ReplaceAll(rendered, "{{.title-filename-truncated}}", title)
	rendered = sanitizeRenderedArchiveFileName(rendered)
	if rendered == "" {
		return fmt.Sprintf("%d.md", meta.MessageID)
	}
	return rendered
}

func sanitizeArchiveFileNameTitle(title string) string {
	var builder strings.Builder
	lastDash := false

	for _, r := range strings.TrimSpace(title) {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			builder.WriteRune(r)
			lastDash = false
		case unicode.IsSpace(r), r == '-', r == '_':
			if builder.Len() > 0 && !lastDash {
				builder.WriteRune('-')
				lastDash = true
			}
		}
	}

	result := strings.Trim(builder.String(), "-")
	result = truncateForFrontMatter(result, 20)
	return strings.Trim(result, "-")
}

func sanitizeRenderedArchiveFileName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}

	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "",
		"?", "",
		"\"", "",
		"<", "",
		">", "",
		"|", "-",
	)
	trimmed = replacer.Replace(trimmed)
	trimmed = strings.Trim(trimmed, ". ")
	return filepath.Base(trimmed)
}

// formatDownloadedFiles 将下载后的媒体路径格式化为 Markdown 图片片段。
// 这样做的原因是隔离展示格式拼接逻辑，避免散落在归档主流程中。
func formatDownloadedFiles(files []string) string {
	var builder strings.Builder
	for _, file := range files {
		builder.WriteString("![](")
		builder.WriteString(file)
		builder.WriteString(") ")
	}
	return builder.String()
}

// persistFile 下载并保存 Telegram 媒体文件，返回可入库的附件元数据。
// 这样做的原因是把媒体 I/O 细节封装在单点，减少归档主流程的噪音与耦合。
func persistFile(ctx context.Context, b *bot.Bot, fileID string, fileUniqueID string, dirname string, outputPath string, messageType Entity.MessageType) *Entity.Attachment {
	params := bot.GetFileParams{FileID: fileID}
	file, err := b.GetFile(ctx, &params)
	if err != nil {
		LogUtils.GetLogger().Printf("获取文件信息失败: %v\n", err)
		return nil
	}

	downloadURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.Token(), file.FilePath)

	resp, err := downloadFile(downloadURL)
	if err != nil {
		LogUtils.GetLogger().Printf("下载文件失败: %v\n", err)
		return nil
	}

	fileName := buildPersistedAttachmentFileName(fileUniqueID, file.FilePath)
	fullOutputDir := filepath.Join(outputPath, "assets", dirname)
	fullOutputFilename := filepath.Join(fullOutputDir, fileName)
	relatedPath := filepath.Join("assets", dirname, fileName)

	FileUtils.OutputResponse(fullOutputDir, fileName, resp)

	size, err := FileUtils.GetFileSize(fullOutputFilename)
	if err != nil {
		size = 0
	}

	return &Entity.Attachment{
		FileName: fileName,
		FilePath: relatedPath,
		FileSize: size,
		Type:     messageType,
	}
}

func buildPersistedAttachmentFileName(fileUniqueID string, remotePath string) string {
	name := sanitizeRenderedArchiveFileName(fileUniqueID)
	if containsLetterOrNumber(name) {
		return ensureFileExtension(name, remotePath)
	}

	// failure fallback to timestamp-based name if unique ID is not usable as filename
	ext := filepath.Ext(remotePath)
	if ext == "" {
		ext = ".dat"
	}

	timestamp := time.Now().Format("20060102_150405") + fmt.Sprintf("_%d", time.Now().UnixNano()%1e6)
	return fmt.Sprintf("%s%s", timestamp, ext)
}

// ensureFileExtension 为无扩展名的文件名补齐远程文件扩展名。
// 这样做的原因是避免丢失后缀导致 R2 对象无法按 MIME 类型被浏览器直接打开。
func ensureFileExtension(name string, remotePath string) string {
	if filepath.Ext(name) != "" {
		return name
	}
	ext := filepath.Ext(remotePath)
	if ext == "" {
		return name
	}
	return name + ext
}

func containsLetterOrNumber(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}
