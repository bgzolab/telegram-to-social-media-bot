package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/internal/Handler"
	"telegram-message-sync-bot/internal/service/albumservice"
	"telegram-message-sync-bot/internal/service/archivemigrationservice"
	"telegram-message-sync-bot/internal/service/attachmentmigrationservice"
	"telegram-message-sync-bot/internal/service/bootstrapservice"
	"telegram-message-sync-bot/internal/service/jsonbackfillservice"
	"telegram-message-sync-bot/internal/service/pipelineservice"
	"telegram-message-sync-bot/pkg/FileUtils"
	"telegram-message-sync-bot/pkg/LogUtils"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/spf13/cobra"
)

// 全局配置
var globalConfig Entity.Config

// 相册聚合服务：start() 完成装配后供默认 handler 登记相册成员。
var globalAlbumService *albumservice.Service

const unauthorizedText = "无权限"

// start 启动 Telegram Bot
func start(botToken string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	opts := []bot.Option{
		bot.WithDefaultHandler(requireAuthorization(defalutHandler)),
		bot.WithMessageTextHandler("/start", bot.MatchTypeExact, requireAuthorization(Handler.Start)),
		bot.WithMessageTextHandler("/status", bot.MatchTypeExact, requireAuthorization(Handler.Version)),
		bot.WithMessageTextHandler("/admin", bot.MatchTypeExact, requireAuthorization(func(ctx context.Context, b *bot.Bot, update *models.Update) {
			Handler.AdminHome(ctx, b, update, globalConfig)
		})),
		bot.WithCallbackQueryDataHandler("admin:", bot.MatchTypePrefix, requireAuthorization(func(ctx context.Context, b *bot.Bot, update *models.Update) {
			Handler.AdminCallback(ctx, b, update, globalConfig)
		})),
	}

	b, err := bot.New(botToken, opts...)
	if err != nil {
		LogUtils.GetLogger().Fatal(err)
	}

	// 相册聚合同步依赖消息已归档入库，因此放在 bot 装配完成后初始化，并立即恢复重启前未投递的分组。
	globalAlbumService = albumservice.New(b, globalConfig)
	globalAlbumService.RecoverPending()

	_, err = b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: []models.BotCommand{
			{Command: "start", Description: "Start bot"},
			{Command: "status", Description: "Check bot status"},
			{Command: "admin", Description: "Open admin panel"},
		},
	})
	if err != nil {
		LogUtils.GetLogger().Fatalf("设置命令失败: %v", err)
	}

	b.Start(ctx)
}

/** 消息默认处理器，默认缓存所有消息
 */
func defalutHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}

	if globalConfig.Output.JSON {
		persistJSON(update)
	}

	pipeline := pipelineservice.NewDefaultPipeline(globalAlbumService)
	pipeline.SetExecutionMode(pipelineservice.ResolveExecutionMode(globalConfig))
	result := pipeline.ProcessUpdate(ctx, b, update, globalConfig)

	if !result.PersistResult.OK {
		LogUtils.GetLogger().Println(result.PersistResult.Message)
	}
	if !result.SyncEnabled {
		LogUtils.GetLogger().Println(result.SyncReason)
	}

	for _, outbound := range result.OutboundMessages {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: outbound.ChatID,
			Text:   outbound.Text,
		})
	}
}

func requireAuthorization(next func(context.Context, *bot.Bot, *models.Update)) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if isUpdateAuthorized(update, globalConfig) {
			next(ctx, b, update)
			return
		}

		respondUnauthorized(ctx, b, update)
	}
}

func isUpdateAuthorized(update *models.Update, config Entity.Config) bool {
	if len(config.AuthorizedUserList) == 0 {
		return true
	}

	userID, ok := resolveActorID(update)
	if !ok {
		return false
	}

	for _, allowedID := range config.AuthorizedUserList {
		if allowedID == userID {
			return true
		}
	}

	return false
}

func resolveActorID(update *models.Update) (int64, bool) {
	if update == nil {
		return 0, false
	}

	if update.Message != nil && update.Message.From != nil {
		return update.Message.From.ID, true
	}

	if update.CallbackQuery != nil {
		return update.CallbackQuery.From.ID, update.CallbackQuery.From.ID != 0
	}

	return 0, false
}

func respondUnauthorized(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil {
		return
	}

	if isGroupChatUpdate(update) {
		if update.CallbackQuery != nil {
			_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
				CallbackQueryID: update.CallbackQuery.ID,
			})
		}

		text := buildUnauthorizedGroupNotification(update)
		for _, chatID := range globalConfig.TargetUserList {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   text,
			})
		}
		return
	}

	if update.CallbackQuery != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			Text:            unauthorizedText,
			ShowAlert:       true,
		})
		return
	}

	if update.Message != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   unauthorizedText,
		})
	}
}

func isGroupChatUpdate(update *models.Update) bool {
	chat, ok := resolveUpdateChat(update)
	if !ok {
		return false
	}

	return chat.Type == "group" || chat.Type == "supergroup"
}

func resolveUpdateChat(update *models.Update) (models.Chat, bool) {
	if update == nil {
		return models.Chat{}, false
	}

	if update.Message != nil {
		return update.Message.Chat, true
	}

	if update.CallbackQuery != nil && update.CallbackQuery.Message.Message != nil {
		return update.CallbackQuery.Message.Message.Chat, true
	}

	return models.Chat{}, false
}

func buildUnauthorizedGroupNotification(update *models.Update) string {
	return fmt.Sprintf(
		"用户 %s 无交互权限，交互内容：%s，不记录数据库，跳过处理",
		resolveActorDisplay(update),
		resolveInteractionContent(update),
	)
}

func resolveActorDisplay(update *models.Update) string {
	if update == nil {
		return "unknown"
	}

	if update.Message != nil && update.Message.From != nil {
		return formatUserDisplay(*update.Message.From)
	}

	if update.CallbackQuery != nil {
		return formatUserDisplay(update.CallbackQuery.From)
	}

	return "unknown"
}

func formatUserDisplay(user models.User) string {
	if user.Username != "" {
		return fmt.Sprintf("@%s(%d)", user.Username, user.ID)
	}

	fullName := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if fullName != "" {
		return fmt.Sprintf("%s(%d)", fullName, user.ID)
	}

	if user.ID != 0 {
		return fmt.Sprintf("%d", user.ID)
	}

	return "unknown"
}

func resolveInteractionContent(update *models.Update) string {
	if update == nil {
		return "空内容"
	}

	if update.Message != nil {
		text := strings.TrimSpace(update.Message.Text)
		if text != "" {
			return text
		}

		caption := strings.TrimSpace(update.Message.Caption)
		if caption != "" {
			return caption
		}
	}

	if update.CallbackQuery != nil {
		data := strings.TrimSpace(update.CallbackQuery.Data)
		if data != "" {
			return data
		}
	}

	return "空内容"
}

func persistJSON(update *models.Update) (bool, string) {
	if update.Message == nil {
		return false, "接受消息为空"
	}

	// 使用 json.Marshal 将对象转换为 JSON 字符串
	jsonData, errJson := json.Marshal(update)
	if errJson != nil {
		fmt.Println("转换 JSON 失败:", errJson)
		return false, "转换 JSON 失败"
	}

	// 使用时间戳生成唯一文件名
	timestamp := time.Now().Format("20060102_150405") + fmt.Sprintf("_%d", time.Now().UnixNano()%1e6)

	FileUtils.OutputString(filepath.Join(globalConfig.Output.JsonDir, time.Now().Format("20060102")),
		fmt.Sprintf("%s%s", timestamp, ".json"),
		string(jsonData))

	return true, "JSON序列化成功"
}

func main() {
	rootCmd := buildRootCommand()
	err := rootCmd.Execute()
	if err != nil {
		return
	}
}

func buildRootCommand() *cobra.Command {
	var configFile string

	var cmdSync = &cobra.Command{
		Use:   "sync",
		Short: "Sync the message from tg bot",
		Long:  `Sync the message from tg bot.`,
		Args:  cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			loadedConfig, err := bootstrapservice.LoadConfig(configFile)
			if err != nil {
				fmt.Printf("加载配置失败: %v\n", err)
				return
			}

			globalConfig = loadedConfig
			fmt.Printf("解析配置成功: 配置内容: %+v\n", globalConfig)

			err = bootstrapservice.InitRuntime(globalConfig)
			if err != nil {
				fmt.Printf("初始化运行时失败: %v\n", err)
				LogUtils.GetLogger().Println(err)
				return
			}

			start(globalConfig.Token)
		},
	}

	var cmdMigrate = &cobra.Command{
		Use:   "migrate",
		Short: "Run archive migration operations",
		Long:  `Run archive migration operations.`,
	}

	var cmdMigrateBackfill = &cobra.Command{
		Use:   "backfill",
		Short: "Backfill local archives from database",
		Long:  `Backfill local archives from database.`,
		Args:  cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			cfg, err := bootstrapservice.LoadConfig(configFile)
			if err != nil {
				fmt.Printf("加载配置失败: %v\n", err)
				return
			}

			err = bootstrapservice.InitRuntime(cfg)
			if err != nil {
				fmt.Printf("初始化运行时失败: %v\n", err)
				LogUtils.GetLogger().Println(err)
				return
			}

			stats, err := archivemigrationservice.BackfillFromDatabase(cfg)
			if err != nil {
				fmt.Printf("DB 全量补齐失败: %v\n", err)
				return
			}

			fmt.Printf("DB 全量补齐完成: %+v\n", stats)
		},
	}

	var cmdMigrateMoveLegacy = &cobra.Command{
		Use:   "move-legacy",
		Short: "Move legacy root markdown files to pending-delete directory",
		Long:  `Move legacy root markdown files to pending-delete directory.`,
		Args:  cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			cfg, err := bootstrapservice.LoadConfig(configFile)
			if err != nil {
				fmt.Printf("加载配置失败: %v\n", err)
				return
			}

			err = bootstrapservice.InitRuntime(cfg)
			if err != nil {
				fmt.Printf("初始化运行时失败: %v\n", err)
				LogUtils.GetLogger().Println(err)
				return
			}

			stats, err := archivemigrationservice.BackupAndMoveLegacySingleFiles(cfg)
			if err != nil {
				fmt.Printf("旧文件迁移到待删除目录失败: %v\n", err)
				return
			}

			fmt.Printf("旧文件迁移到待删除目录完成: %+v\n", stats)
		},
	}

	var cmdMigrateJSONToDB = &cobra.Command{
		Use:   "json-to-db",
		Short: "Import archived JSON updates into database",
		Long:  `Import archived JSON updates into database.`,
		Args:  cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			cfg, err := bootstrapservice.LoadConfig(configFile)
			if err != nil {
				fmt.Printf("加载配置失败: %v\n", err)
				return
			}

			err = bootstrapservice.InitRuntime(cfg)
			if err != nil {
				fmt.Printf("初始化运行时失败: %v\n", err)
				LogUtils.GetLogger().Println(err)
				return
			}

			stats, err := jsonbackfillservice.BackfillFromJSON(cfg)
			if err != nil {
				fmt.Printf("JSON 补录数据库失败: %v\n", err)
				return
			}

			fmt.Printf("JSON 补录数据库完成: %+v\n", stats)
		},
	}

	var cmdMigrateMediaGroups = &cobra.Command{
		Use:   "media-groups",
		Short: "Backfill media_group_id for archived messages from JSON",
		Long:  `Backfill media_group_id for archived messages from JSON.`,
		Args:  cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			cfg, err := bootstrapservice.LoadConfig(configFile)
			if err != nil {
				fmt.Printf("加载配置失败: %v\n", err)
				return
			}

			err = bootstrapservice.InitRuntime(cfg)
			if err != nil {
				fmt.Printf("初始化运行时失败: %v\n", err)
				LogUtils.GetLogger().Println(err)
				return
			}

			stats, err := jsonbackfillservice.BackfillMediaGroupIDs(cfg)
			if err != nil {
				fmt.Printf("相册分组回填失败: %v\n", err)
				return
			}

			fmt.Printf("相册分组回填完成: %+v\n", stats)
		},
	}

	var cmdMigrateAttachmentsToR2 = &cobra.Command{
		Use:   "attachments-to-r2",
		Short: "Upload image attachments to Cloudflare R2 and rewrite Markdown",
		Long:  `Upload image attachments to Cloudflare R2 and rewrite Markdown.`,
		Args:  cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			cfg, err := bootstrapservice.LoadConfig(configFile)
			if err != nil {
				fmt.Printf("加载配置失败: %v\n", err)
				return
			}

			err = bootstrapservice.InitRuntime(cfg)
			if err != nil {
				fmt.Printf("初始化运行时失败: %v\n", err)
				LogUtils.GetLogger().Println(err)
				return
			}

			stats, err := attachmentmigrationservice.BackfillAttachmentsToR2(context.Background(), cfg)
			if err != nil {
				fmt.Printf("附件迁移失败: %v\n", err)
				return
			}

			fmt.Printf("附件迁移完成: %+v\n", stats)
		},
	}

	cmdMigrateBackfill.Flags().StringVarP(&configFile, "config", "c", "./config/config.yaml", "config for bot.")
	err := cmdMigrateBackfill.MarkFlagRequired("config")
	if err != nil {
		return cmdMigrate
	}
	cmdMigrateJSONToDB.Flags().StringVarP(&configFile, "config", "c", "./config/config.yaml", "config for bot.")
	err = cmdMigrateJSONToDB.MarkFlagRequired("config")
	if err != nil {
		return cmdMigrate
	}
	cmdMigrateAttachmentsToR2.Flags().StringVarP(&configFile, "config", "c", "./config/config.yaml", "config for bot.")
	err = cmdMigrateAttachmentsToR2.MarkFlagRequired("config")
	if err != nil {
		return cmdMigrate
	}
	cmdMigrateMoveLegacy.Flags().StringVarP(&configFile, "config", "c", "./config/config.yaml", "config for bot.")
	err = cmdMigrateMoveLegacy.MarkFlagRequired("config")
	if err != nil {
		return cmdMigrate
	}

	cmdMigrateMediaGroups.Flags().StringVarP(&configFile, "config", "c", "./config/config.yaml", "config for bot.")
	err = cmdMigrateMediaGroups.MarkFlagRequired("config")
	if err != nil {
		return cmdMigrate
	}

	cmdMigrate.AddCommand(cmdMigrateBackfill)
	cmdMigrate.AddCommand(cmdMigrateJSONToDB)
	cmdMigrate.AddCommand(cmdMigrateAttachmentsToR2)
	cmdMigrate.AddCommand(cmdMigrateMoveLegacy)
	cmdMigrate.AddCommand(cmdMigrateMediaGroups)

	cmdSync.Flags().StringVarP(&configFile, "config", "c", "./config/config.yaml", "config for bot.")
	err = cmdSync.MarkFlagRequired("config")
	if err != nil {
		return cmdSync
	}

	var rootCmd = &cobra.Command{Use: "tg"}
	rootCmd.AddCommand(cmdSync)
	rootCmd.AddCommand(cmdMigrate)

	return rootCmd

	//message := "Hello world from script!"
	//fmt.Println(SocialMediaUtils.SendBlueSky(globalConfig, message))
	//fmt.Println(SocialMediaUtils.SendTwitter(globalConfig, message))
	//fmt.Println(SocialMediaUtils.SendMastodon(globalConfig, message))

}
