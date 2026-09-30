package Entity

/** 与配置文件一一对应
 */
type Config struct {
	Output struct {
		JSON       bool   `yaml:"json"`
		JsonDir    string `yaml:"json_dir"`
		PersonDir  string `yaml:"person_dir"`
		ChannelDir string `yaml:"channel_dir"`
	} `yaml:"output"`

	Log struct {
		Enable bool   `yaml:"enable"`
		Dir    string `yaml:"dir"`
	} `yaml:"log"`

	Template struct {
		Dir              string `yaml:"dir"`
		FileNameTemplate string `yaml:"file_name_template"`
	} `yaml:"template"`

	Token string `yaml:"token"`

	TargetUserList     []int64 `yaml:"targetUserList"`
	AuthorizedUserList []int64 `yaml:"authorizedUserList"`

	Pipeline struct {
		ExecutionMode string `yaml:"executionMode"`
	} `yaml:"pipeline"`

	R2 struct {
		Enable           bool   `yaml:"enable"`
		ServerAddress    string `yaml:"server_address"`
		Bucket           string `yaml:"bucket"`
		AccessKeyID      string `yaml:"access_key_id"`
		SecretAccessKey  string `yaml:"secret_access_key"`
		Path             string `yaml:"path"`
		PublicAddress    string `yaml:"public_address"`
	} `yaml:"r2"`

	SocialMediaSync struct {
		Enable        bool     `yaml:"enable"`
		TargetChannel []string `yaml:"targetChannel"`

		// AlbumDebounceSeconds 是相册聚合静默窗口，单位秒；0 或缺省时使用默认值。
		AlbumDebounceSeconds int `yaml:"albumDebounceSeconds"`

		Mastodon struct {
			Enable       bool   `yaml:"enable"`
			Instance     string `yaml:"instance"`
			ClientId     string `yaml:"clientId"`
			ClientSecret string `yaml:"clientSecret"`
			AccessToken  string `yaml:"accessToken"`
		} `yaml:"mastodon"`

		Twitter struct {
			Enable            bool   `yaml:"enable"`
			OauthToken        string `yaml:"oauthToken"`
			OauthTokenSecret  string `yaml:"oauthTokenSecret"`
			GotwtApiKey       string `yaml:"gotwtApiKey"`
			GotwtApiKeySecret string `yaml:"gotwtApiKeySecret"`
		} `yaml:"twitter"`

		BlueSky struct {
			Enable     bool   `yaml:"enable"`
			Identifier string `yaml:"identifier"`
			Password   string `yaml:"password"`
		} `yaml:"bluesky"`
	} `yaml:"socialMediaSync"`
}
