package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrCancelled = errors.New("已取消配置")

type Config struct {
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	APIKeyEnv  string `json:"api_key_env,omitempty"`
	Permission string `json:"permission"`

	DefaultProfile string             `json:"default_profile,omitempty"`
	Profiles       map[string]Profile `json:"profiles,omitempty"`
	Routing        RoutingConfig      `json:"routing,omitzero"`
}

func Path() (string, error) {
	d, e := os.UserConfigDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(d, "kero", "config.json"), nil
}
func Load() (Config, error) {
	var c Config
	p, e := Path()
	if e != nil {
		return c, e
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if e = c.Validate(); e != nil {
		return c, e
	}
	c.applyDefaultProfile()
	return c, nil
}
func (c Config) Validate() error {
	defaultName := c.defaultProfileName()
	if _, e := c.SelectProfile(defaultName); e != nil {
		return e
	}
	for _, name := range c.ProfileNames() {
		if name == "" {
			return errors.New("profile names must be nonempty and have no surrounding whitespace")
		}
		if name == defaultName {
			continue
		}
		if _, e := c.SelectProfile(name); e != nil {
			return e
		}
	}
	if e := c.validateRouting(); e != nil {
		return e
	}
	switch c.Permission {
	case "ask", "workspace", "auto":
		return nil
	}
	return errors.New("permission must be ask, workspace, or auto")
}
func Save(c Config) error {
	if e := c.Validate(); e != nil {
		return e
	}
	c.applyDefaultProfile()
	p, e := Path()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".config-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(append(b, '\n'))
	}
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), p)
}

// Configure guides the first run or edits the current default profile.
// An input error, including EOF, cancels without saving.
func Configure(in *bufio.Reader, old Config) (Config, error) {
	c := old
	name := c.defaultProfileName()
	profile, named := c.Profiles[name]
	if !named {
		profile = Profile{BaseURL: c.BaseURL, Model: c.Model, APIKeyEnv: c.APIKeyEnv}
	}
	var err error
	if profile, err = configureProfile(in, profile, false); err != nil {
		return old, err
	}
	if c.Permission, err = configurePermission(in, c.Permission); err != nil {
		return old, err
	}
	c.BaseURL, c.Model, c.APIKeyEnv = profile.BaseURL, profile.Model, profile.APIKeyEnv
	if named {
		c.Profiles = maps.Clone(c.Profiles)
		c.Profiles[name] = profile
		c.DefaultProfile = name
	}
	if err = Save(c); err != nil {
		return old, err
	}
	return c, nil
}

// ConfigureMenu manages profiles and permissions in one session. Changes are
// saved only when the user selects save; EOF or cancellation discards them.
func ConfigureMenu(in *bufio.Reader, old Config) (Config, error) {
	if len(old.Profiles) == 0 && old.BaseURL == "" && old.Model == "" {
		return Configure(in, old)
	}
	c := old
	c.Profiles = maps.Clone(old.Profiles)
	if c.Profiles == nil {
		c.Profiles = make(map[string]Profile)
	}
	if c.hasLegacyProfile() {
		if _, exists := c.Profiles[legacyProfile]; !exists {
			c.Profiles[legacyProfile] = Profile{BaseURL: c.BaseURL, Model: c.Model, APIKeyEnv: c.APIKeyEnv}
		}
	}
	c.DefaultProfile = old.defaultProfileName()
	for {
		fmt.Printf("\n配置方案：%s（默认：%s）\n", strings.Join(c.ProfileNames(), "、"), c.DefaultProfile)
		fmt.Println("1. 编辑或新增方案  2. 选择默认方案  3. 修改权限  4. 保存  0. 取消")
		choice, err := readConfigLine(in, "请选择", "")
		if err != nil {
			return old, err
		}
		switch choice {
		case "1":
			name, err := askConfig(in, "方案名称（输入现有名称编辑，或输入新名称）", c.DefaultProfile, false, func(s string) string {
				if s == "" {
					return "名称不能为空"
				}
				return ""
			})
			if err != nil {
				return old, err
			}
			profile, err := configureProfile(in, c.Profiles[name], true)
			if err != nil {
				return old, err
			}
			c.Profiles[name] = profile
		case "2":
			name, err := askConfig(in, "默认方案名称", c.DefaultProfile, false, func(s string) string {
				if _, ok := c.Profiles[s]; !ok {
					return "方案不存在，请输入列表中的名称"
				}
				return ""
			})
			if err != nil {
				return old, err
			}
			c.DefaultProfile = name
		case "3":
			permission, err := configurePermission(in, c.Permission)
			if err != nil {
				return old, err
			}
			c.Permission = permission
		case "4":
			c.applyDefaultProfile()
			if err := Save(c); err != nil {
				return old, err
			}
			return c, nil
		case "0":
			return old, ErrCancelled
		default:
			fmt.Println("请输入 0、1、2、3 或 4。")
		}
	}
}

func readConfigLine(in *bufio.Reader, label, value string) (string, error) {
	if value == "" {
		value = "未设置"
	}
	fmt.Printf("%s [%s]：", label, value)
	s, err := in.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func askConfig(in *bufio.Reader, label, value string, clear bool, validate func(string) string) (string, error) {
	for {
		s, err := readConfigLine(in, label, value)
		if err != nil {
			return "", err
		}
		if s == "" {
			s = value
		} else if clear && s == "-" {
			s = ""
		}
		if reason := validate(s); reason != "" {
			fmt.Println("输入无效：" + reason + "，请重试。")
			continue
		}
		return s, nil
	}
}

func configureProfile(in *bufio.Reader, p Profile, context bool) (Profile, error) {
	var err error
	fmt.Println("配置模型服务。按回车保留方括号中的当前值；首次配置请填写必填项。")
	p.BaseURL, err = askConfig(in, "服务地址（必填，例如 https://api.openai.com/v1 或 http://localhost:11434/v1）", p.BaseURL, false, func(s string) string {
		if (Profile{BaseURL: s, Model: "model"}).validate() != nil {
			return "请输入不含凭据、查询或片段的 HTTP(S) 地址"
		}
		return ""
	})
	if err != nil {
		return p, err
	}
	p.Model, err = askConfig(in, "模型 ID（必填，例如 gpt-4o）", p.Model, false, func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "模型 ID 不能为空"
		}
		return ""
	})
	if err != nil {
		return p, err
	}
	fmt.Println("API 密钥请存入环境变量；这里只填写变量名，不填写密钥。无需认证时可留空，输入 - 清除已有设置。")
	p.APIKeyEnv, err = askConfig(in, "API 密钥环境变量名（例如 OPENAI_API_KEY，可选）", p.APIKeyEnv, true, func(s string) string {
		if strings.ContainsAny(s, "=\x00") {
			return "环境变量名不能包含 = 或空字符"
		}
		return ""
	})
	if err != nil {
		return p, err
	}
	if context {
		value, e := askConfig(in, "上下文 token 上限（0 表示未指定）", strconv.Itoa(p.ContextTokens), false, func(s string) string {
			n, e := strconv.Atoi(s)
			if e != nil || n < 0 {
				return "请输入非负整数"
			}
			return ""
		})
		if e != nil {
			return p, e
		}
		p.ContextTokens, _ = strconv.Atoi(value)
	}
	return p, nil
}

func configurePermission(in *bufio.Reader, current string) (string, error) {
	if current == "" {
		current = "ask"
	}
	fmt.Println("权限模式：ask 每次确认编辑和命令；workspace 允许工作区编辑、命令需确认；auto 自动允许编辑和命令（命令可访问工作区以外的内容）。")
	return askConfig(in, "权限模式（ask / workspace / auto）", current, false, func(s string) string {
		switch s {
		case "ask", "workspace", "auto":
			return ""
		}
		return "请选择 ask、workspace 或 auto"
	})
}
