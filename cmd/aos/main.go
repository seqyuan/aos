// aos — 对象存储上传/下载/浏览命令行工具（当前后端为火山云 TOS）。
//
// 用法示例：
//
//	aos ls tos://example-bucket/ACME2026001           查看目录树
//	aos cp ./dataset tos://example-bucket/ACME2026001/PM-xxx-01/dataset   上传
//	aos cp tos://example-bucket/ACME2026001/PM-xxx-01/dataset /local      下载
//	aos stat                                            查询任务状态（sqlite）
//	aos check                                           连接与权限诊断
//	aos config set -ak AK... -sk SK...                  配置凭据
package main

import (
	"context"
	"errors"
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

// version 由构建注入（git tag / CI）；本地 go build 时为当前版本号。
var version = "v0.3.0"

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
	case "cp", "copy":
		return cmdCP(rest)
	case "stat":
		return cmdStat(rest)
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
	if err := validateNoFlagAsValue(fs, args); err != nil {
		// flag 包的解析错误会自己打印，这里需手动打印 + usage
		fmt.Fprintln(os.Stderr, err)
		fs.Usage()
		return false, err
	}
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	return true, nil
}

// validateNoFlagAsValue 防止非布尔 flag 把另一个 flag 当作自己的值吞掉。
// Go 标准 flag 包对 "-d -q" 会解析成 d="-q"（q 不生效），属于用户漏写参数；
// 这里提前拦截并给出标准错误，除非用户用 -flag=值 形式显式传以 - 开头的值。
func validateNoFlagAsValue(fs *flag.FlagSet, args []string) error {
	boolFlags, known := map[string]bool{}, map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		known[f.Name] = true
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			boolFlags[f.Name] = true
		}
	})
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			continue
		}
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") || boolFlags[name] {
			continue
		}
		if i+1 < len(args) && strings.HasPrefix(args[i+1], "-") {
			next := strings.TrimLeft(args[i+1], "-")
			if known[next] {
				return fmt.Errorf("flag needs an argument: -%s（其后紧跟另一个 flag -%s；如需传以 - 开头的值请用 -%s=值）", name, next, name)
			}
			// 未知的 - 前缀参数：负数（如 -max-depth -1）留给 flag 包正常解析；
			// 其余情况大概率是用户拼错/漏写的 flag，提前拦截避免被吞成前一个 flag 的值。
			if next != "" && !isNegativeNumber(args[i+1]) {
				return fmt.Errorf("flag needs an argument: -%s（其后紧跟 %s，可能是拼写错误的 flag；如需传以 - 开头的值请用 -%s=值）", name, args[i+1], name)
			}
		}
	}
	return nil
}

// isNegativeNumber 判断是否为纯负数形式（允许负数值 flag，如 -max-depth -1）。
func isNegativeNumber(s string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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
			// 值以下一个参数形式给出时，不能以 "-" 开头（否则会把布尔 flag 当值吞掉，
			// 例如 "-d -q" 应报 -d 缺参，而不是把 -d 赋成 "-q"）
			if !boolFlags[name] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
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
	// 显式 tos://bucket/... 路径时 bucket 取自路径，无需配置默认 bucket；
	// 否则（纯前缀形式）必须校验默认 bucket。
	if strings.Contains(fs.Arg(0), "://") {
		if err := cfg.ValidateAuth(); err != nil {
			fmt.Fprintf(os.Stderr, "aos ls: %v\n（运行 aos config set 配置凭据）\n", err)
			return 1
		}
	} else if err := cfg.Validate(); err != nil {
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
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "aos ls: 列表超时（最长 2 分钟）。请缩小路径范围后重试，或检查网络")
			return 1
		}
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

	// 依次尝试：配置的 endpoint -> 按 region 推导的公网 endpoint（避免内网/异 region 配置下误探测）
	type try struct{ ep, region string }
	tries := []try{{cfg.Endpoint, cfg.Region}}
	publicEP := ""
	if cfg.Region != "" {
		derived := "tos-" + cfg.Region + ".volces.com"
		if cfg.Endpoint != derived {
			publicEP = derived
		}
	} else if cfg.Endpoint != config.DefaultEndpoint {
		publicEP = config.DefaultEndpoint
	}
	if publicEP != "" {
		region := cfg.Region
		if region == "" {
			region = config.DefaultRegion
		}
		tries = append(tries, try{publicEP, region})
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
  aos cp <源> [<目标>] [选项]      上传/下载（云上路径带 tos:// 前缀，方向由参数顺序决定）
  aos ls <tos路径> [选项]          以目录树形式列出目标路径下的文件
  aos stat [选项]                  查询上传历史（默认：中断/失败 + 近 2 天；-a 全部）
  aos check [选项]                 诊断连接与权限
  aos config [set] [选项]          查看/配置凭据
  aos version                      版本号

cp 示例（上传：本地在前）:
  aos cp ./dataset tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset
  aos cp ./dataset tos:///ACME2026001/PM-ACME2026001-01/dataset   # bucket 用配置默认
  aos cp dataset.zip tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset.zip

cp 示例（下载：tos 在前）:
  aos cp tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset /local
  aos cp tos://example-bucket/ACME2026001/PM-ACME2026001-01/dataset    # 下载到当前目录
  aos cp /data/project1/dataset            # 单参数本地路径：还原上次上传的数据

ls 示例:
  aos ls tos://example-bucket/ACME2026001
  aos ls tos:///ACME2026001/PM-ACME2026001-01/dataset

stat 示例:
  aos stat                # 中断/失败 + 近 2 天的任务
  aos stat -a             # 全部任务
  aos stat -id 3          # 某次任务的详情（错误信息等）

行为说明:
  - 上传/下载任务均记录到 sqlite（默认 ~/.config/aos.db，-db/AOS_DB 可改）；-no-record 可关闭
  - 下载在落盘根目录写入 .aos/manifest.db（按 object key + ETag 判断已完成，不比大小）
  - 上传目标前缀直接铺入：文件 key = 目标前缀 + 本地相对路径（目录默认递归）
  - 大文件（≥5MB）分片上传/下载；下载默认断点续传（checkpoint 存于 .aos/checkpoints/）
  - 分片参数 -ps（大小，默认 20MB）、-p（单文件分片并发，默认 4）、-j（文件级并发，默认按 CPU）
  - 网络请求失败自动指数退避重试 2 次
  - 软链接默认不跟随上传（避免误传共享大文件）；-follow-links 溯源上传
  - 路径自动规范化（./abc//de -> abc/de）

配置说明:
  配置文件默认位于 aos 二进制同目录的 aos.json，随二进制一起拷贝即可使用。
  内网/专线环境用 endpoint tos-cn-beijing.ivolces.com；公网用 tos-cn-beijing.volces.com。
  可用环境变量 AOS_AK / AOS_SK / AOS_ENDPOINT / AOS_REGION / AOS_BUCKET / AOS_DB 覆盖。
`, version)
}
