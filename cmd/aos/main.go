// aos — 对象存储上传/下载/浏览命令行工具（当前后端为火山云 TOS）。
//
// 用法示例：
//
//	aos ls tos://example-bucket/ACME2026001           查看目录树
//	aos cp -spi PM-ACME2026001-01 -d ./dataset         上传（自动推导 contract）
//	aos down -spi PM-ACME2026001-01 -d /local           下载
//	aos stat                                            查询任务状态（sqlite）
//	aos restore -id 3 -d /local                         按软链接记录还原 symlink
//	aos check                                           连接与权限诊断
//	aos config set -ak AK... -sk SK...                  配置凭据
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/seqyuan/aos/internal/config"
	"github.com/seqyuan/aos/internal/human"
	"github.com/seqyuan/aos/internal/tosx"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// version 由构建注入（git tag / CI）；本地 go build 时为 dev。
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return 0
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "ls", "list":
		return cmdLS(rest)
	case "cp", "upload":
		return cmdCP(rest)
	case "down", "download", "dl":
		return cmdDown(rest)
	case "stat":
		return cmdStat(rest)
	case "restore":
		return cmdRestore(rest)
	case "config":
		return cmdConfig(rest)
	case "check":
		return cmdCheck(rest)
	case "version", "-version", "--version", "-v":
		fmt.Printf("aos %s\n", version)
		return 0
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "aos: 未知命令 %q\n\n", cmd)
		printUsage(os.Stderr)
		return 2
	}
}

// ---------------------------------------------------------------------------
// 公共选项

type baseFlags struct {
	configPath string
	endpoint   string
	region     string
	bucket     string
}

func (b *baseFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&b.configPath, "config", "", "配置文件路径（默认：二进制同目录 aos.json）")
	fs.StringVar(&b.endpoint, "endpoint", "", "覆盖 endpoint（如 tos-cn-beijing.ivolces.com）")
	fs.StringVar(&b.region, "region", "", "覆盖 region（如 cn-beijing）")
	fs.StringVar(&b.bucket, "bucket", "", "覆盖 bucket 名称")
}

func (b *baseFlags) loadConfig() (config.Config, string, error) {
	path, err := config.ResolvePath(b.configPath)
	if err != nil {
		return config.Config{}, "", err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return cfg, path, err
	}
	if b.endpoint != "" {
		cfg.Endpoint = strings.TrimSuffix(strings.TrimPrefix(b.endpoint, "https://"), "/")
	}
	if b.region != "" {
		cfg.Region = b.region
	}
	if b.bucket != "" {
		cfg.Bucket = b.bucket
	}
	return cfg, path, nil
}

// newSignalCtx 返回带 Ctrl+C 取消的上下文。
func newSignalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func parseFlagSet(fs *flag.FlagSet, args []string, usage string) (bool, error) {
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, usage)
		fs.PrintDefaults()
	}
	args = reorderArgs(fs, args) // 位置参数可出现在任意位置
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	return true, nil
}

// reorderArgs 将位置参数挪到末尾，使 -flag 可以出现在命令行任意位置。
// 例如：aos ls tos://xxx -endpoint yyy 中的 -endpoint 也能被解析。
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	boolFlags := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			boolFlags[f.Name] = true
		}
	})
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") {
				continue // -flag=value 形式
			}
			if !boolFlags[name] && i+1 < len(args) {
				flags = append(flags, args[i+1]) // flag 的值
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

// ---------------------------------------------------------------------------
// aos ls

func cmdLS(args []string) int {
	fs := flag.NewFlagSet("aos ls", flag.ContinueOnError)
	var b baseFlags
	b.register(fs)
	maxDepth := fs.Int("max-depth", 0, "最大显示深度（0 表示不限制）")
	showMod := fs.Bool("m", false, "显示文件修改时间")
	if ok, err := parseFlagSet(fs, args, "用法: aos ls <tos路径> [选项]\n\n示例:\n  aos ls tos://example-bucket/ACME2026001\n  aos ls ACME2026001/PM-ACME2026001-01/dataset"); !ok {
		return 2
	} else if err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "aos ls: 需要 1 个参数（tos 路径）")
		fs.Usage()
		return 2
	}

	cfg, _, err := b.loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos ls: %v\n", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "aos ls: %v\n（运行 aos config set 配置凭据）\n", err)
		return 1
	}
	ctx, cancel := newSignalCtx()
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 2*time.Minute) // ls 最多 2 分钟
	defer cancel()

	client, err := tosx.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos ls: %v\n", err)
		return 1
	}
	if err := tosx.LS(ctx, client, cfg, tosx.LSOptions{
		Path:     fs.Arg(0),
		MaxDepth: *maxDepth,
		ShowMod:  *showMod,
	}, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "aos ls: %v\n", err)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// aos config

func cmdConfig(args []string) int {
	if len(args) > 0 && args[0] == "set" {
		return cmdConfigSet(args[1:])
	}
	if len(args) > 0 && args[0] == "path" {
		p, err := config.ResolvePath("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "aos config: %v\n", err)
			return 1
		}
		fmt.Println(p)
		return 0
	}
	// 默认展示当前配置
	path, err := config.ResolvePath("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos config: %v\n", err)
		return 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos config: %v\n", err)
		return 1
	}
	fmt.Printf("配置文件: %s\n", path)
	fmt.Printf("  endpoint:  %s\n", cfg.Endpoint)
	fmt.Printf("  region:    %s\n", cfg.Region)
	fmt.Printf("  bucket:    %s\n", cfg.Bucket)
	fmt.Printf("  access_key: %s\n", config.MaskSecret(cfg.AccessKey))
	fmt.Printf("  secret_key: %s\n", config.MaskSecret(cfg.SecretKey))
	return 0
}

func cmdConfigSet(args []string) int {
	fs := flag.NewFlagSet("aos config set", flag.ContinueOnError)
	var b baseFlags
	b.register(fs)
	ak := fs.String("ak", "", "Access Key ID")
	sk := fs.String("sk", "", "Secret Access Key")
	if ok, err := parseFlagSet(fs, args, "用法: aos config set -ak <AK> -sk <SK> [选项]\n\n示例:\n  aos config set -ak AKLTMxxx -sk WXpaxxx -endpoint https://tos-cn-beijing.ivolces.com -bucket example-bucket"); !ok {
		return 2
	} else if err != nil {
		return 2
	}
	if *ak == "" || *sk == "" {
		fmt.Fprintln(os.Stderr, "aos config set: 需要 -ak 与 -sk 参数")
		fs.Usage()
		return 2
	}

	path, err := config.ResolvePath(b.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos config set: %v\n", err)
		return 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos config set: %v\n", err)
		return 1
	}
	cfg.AccessKey = *ak
	cfg.SecretKey = *sk
	if b.endpoint != "" {
		cfg.Endpoint = strings.TrimSuffix(strings.TrimPrefix(b.endpoint, "https://"), "/")
	}
	if b.region != "" {
		cfg.Region = b.region
	}
	if b.bucket != "" {
		cfg.Bucket = b.bucket
	}
	if err := cfg.Save(path); err != nil {
		fmt.Fprintf(os.Stderr, "aos config set: %v\n", err)
		return 1
	}
	fmt.Printf("已写入配置: %s\n", path)
	fmt.Printf("  endpoint=%s region=%s bucket=%s ak=%s\n", cfg.Endpoint, cfg.Region, cfg.Bucket, config.MaskSecret(cfg.AccessKey))
	return 0
}

// ---------------------------------------------------------------------------
// aos check

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("aos check", flag.ContinueOnError)
	var b baseFlags
	b.register(fs)
	if ok, err := parseFlagSet(fs, args, "用法: aos check [选项]\n\n诊断 TOS 连接与权限。"); !ok {
		return 2
	} else if err != nil {
		return 2
	}

	cfg, path, err := b.loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos check: %v\n", err)
		return 1
	}
	fmt.Printf("配置文件: %s\n", path)
	fmt.Printf("endpoint: %s\n", cfg.Endpoint)
	fmt.Printf("region:   %s\n", cfg.Region)
	fmt.Printf("bucket:   %s\n", cfg.Bucket)
	fmt.Printf("access_key: %s\n", config.MaskSecret(cfg.AccessKey))
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		return 1
	}

	ctx, cancel := newSignalCtx()
	defer cancel()

	// 依次尝试：配置的 endpoint -> 公网 endpoint
	type try struct{ ep, region string }
	tries := []try{{cfg.Endpoint, cfg.Region}}
	publicEP := ""
	if cfg.Endpoint != "tos-cn-beijing.volces.com" {
		publicEP = "tos-cn-beijing.volces.com"
	}
	if publicEP != "" {
		tries = append(tries, try{publicEP, "cn-beijing"})
	}

	for i, t := range tries {
		if i > 0 {
			fmt.Printf("\n—— 尝试公网 endpoint: %s ——\n", t.ep)
		}
		client, err := tosx.NewClient(config.Config{
			Endpoint:  t.ep,
			Region:    t.region,
			Bucket:    cfg.Bucket,
			AccessKey: cfg.AccessKey,
			SecretKey: cfg.SecretKey,
		})
		if err != nil {
			fmt.Printf("❌ 创建客户端失败: %v\n", err)
			continue
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
		out, err := client.ListObjectsType2(probeCtx, &tos.ListObjectsType2Input{
			Bucket:  cfg.Bucket,
			MaxKeys: 7,
		})
		probeCancel()
		if err == nil {
			fmt.Printf("✅ 连接与权限正常！bucket=%s 可列出对象\n", cfg.Bucket)
			for i, o := range out.Contents {
				if i >= 7 {
					break
				}
				fmt.Printf("   %s (%s)\n", o.Key, human.Size(o.Size))
			}
			return 0
		}
		fmt.Printf("❌ %v\n", tosx.FriendlyError(err))
	}
	fmt.Fprintln(os.Stderr, "\n提示: 若为 Access Denied，请在火山云控制台为子账号授予 TOS 权限；若连接超时，请检查网络（内网环境使用 tos-cn-beijing.ivolces.com）。")
	return 1
}

// ---------------------------------------------------------------------------
// usage

func printUsage(w *os.File) {
	fmt.Fprintf(w, `aos %s — 对象存储上传/下载/浏览工具（当前后端：火山云 TOS）

用法:
  aos ls <tos路径> [选项]            以目录树形式列出目标路径下的文件
  aos cp [选项]                      上传本地目录/文件到 TOS（记录到 sqlite；upload 为别名）
  aos down [选项]                    下载（支持 -d/-spi/-id 定位，自动还原软链接；download/dl 为别名）
  aos stat [选项]                    查询 cp 任务状态（done/break，含进度）
  aos restore [选项]                 手动还原软链接（down 已自动执行）
  aos check [选项]                   诊断连接与权限
  aos config [set] [选项]            查看/配置凭据
  aos version                        版本号

ls 示例:
  aos ls tos://example-bucket/ACME2026001
  aos ls ACME2026001/PM-ACME2026001-01/dataset

cp 示例:
  aos cp -contract ACME2026001 -spi PM-ACME2026001-01 -d /path/project1/dataset
  aos cp -spi PM-ACME2026001-01 -d ./dataset        # -contract 自动推导
  aos cp -spi PM-ACME2026001-01 -d ./dataset -dry-run

down 示例:
  aos down tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset -d /local
  aos down -spi PM-ACME2026001-01                  # 自动探测远端文件夹，存到 ./dataset/
  aos down -d /data/project1/dataset                     # 按 cp 时的路径回查任务，下载回该路径
  aos down -id 3 -d /local                              # 按任务 ID 下载
  # 下载完成后自动还原软链接（-no-restore 关闭）

stat 示例:
  aos stat                                               # 最近任务列表
  aos stat -spi PM-ACME2026001-01                  # 某子项目所有任务
  aos stat -id 3                                         # 任务详情 + 软链接记录

restore 示例:
  aos restore -id 3 -d /local/dataset                    # 还原任务 3 的软链接到本地

行为说明:
  - cp 任务记录写入 sqlite（默认 ~/.config/aos.db，-db/AOS_DB 可改）
  - down 在下载根目录写入 .aos/manifest.db（按 object key + ETag 判断已完成，不比大小）
  - 本地软链接不溯源：上传同名文本文件，内容为链接目标地址；task_links 表记录以便还原
  - 路径自动规范化（./abc//de -> abc/de）

配置说明:
  配置文件默认位于 aos 二进制同目录的 aos.json，随二进制一起拷贝即可使用。
  内网/专线环境用 endpoint tos-cn-beijing.ivolces.com；公网用 tos-cn-beijing.volces.com。
  可用环境变量 AOS_AK / AOS_SK / AOS_ENDPOINT / AOS_REGION / AOS_BUCKET / AOS_DB 覆盖。
`, version)
}
