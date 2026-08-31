package tosx

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/seqyuan/aos/internal/config"
	"github.com/seqyuan/aos/internal/human"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// LSOptions ls 命令选项。
type LSOptions struct {
	Path     string // 用户输入的 tos 路径
	MaxDepth int    // 最大显示深度，<=0 表示不限制
	ShowMod  bool   // 是否显示修改时间
}

// treeNode 目录树节点。
type treeNode struct {
	name     string
	isDir    bool
	size     int64
	mod      time.Time
	children map[string]*treeNode
}

func newDir(name string) *treeNode {
	return &treeNode{name: name, isDir: true, children: map[string]*treeNode{}}
}

func (n *treeNode) ensureDir(name string) *treeNode {
	if c, ok := n.children[name]; ok {
		return c
	}
	c := newDir(name)
	n.children[name] = c
	return c
}

// LS 列出 tos 路径下的文件并打印目录树。
func LS(ctx context.Context, client *tos.ClientV2, cfg config.Config, opt LSOptions, w io.Writer) error {
	tp, err := ParseTOSPath(opt.Path, cfg.Bucket)
	if err != nil {
		return err
	}

	objs, err := ListAll(ctx, client, tp.Bucket, tp.Prefix)
	if err != nil {
		return err
	}

	root := newDir(strings.TrimSuffix(tp.Prefix, "/"))
	if root.name == "" {
		root.name = "/"
	}
	var fileCount, dirCount int
	var totalBytes int64

	for _, o := range objs {
		key := o.Key
		if key == "" {
			continue
		}
		// 目录占位对象（key 以 / 结尾且 size 为 0）不当作文件
		if strings.HasSuffix(key, "/") && o.Size == 0 {
			continue
		}
		rel := strings.TrimPrefix(key, tp.Prefix)
		rel = strings.TrimSuffix(rel, "/")
		if rel == "" {
			continue
		}
		segments := strings.Split(rel, "/")

		node := root
		for _, seg := range segments[:len(segments)-1] {
			if seg == "" {
				continue
			}
			node = node.ensureDir(seg)
		}
		name := segments[len(segments)-1]
		node.children[name] = &treeNode{
			name:     name,
			size:     o.Size,
			mod:      o.LastModified,
			children: map[string]*treeNode{},
		}
		fileCount++
		totalBytes += o.Size
	}

	// 统计目录数
	var countDirs func(n *treeNode)
	countDirs = func(n *treeNode) {
		for _, c := range n.children {
			if c.isDir {
				dirCount++
				countDirs(c)
			}
		}
	}
	countDirs(root)

	// 打印
	fmt.Fprintf(w, "tos://%s/%s\n", tp.Bucket, strings.TrimSuffix(tp.Prefix, "/"))
	printNode(w, root, "", true, opt.MaxDepth, 0, opt.ShowMod)

	if fileCount == 0 {
		fmt.Fprintf(w, "\n（空目录，共 0 个文件）\n")
	} else {
		fmt.Fprintf(w, "\n%d 个文件, %d 个目录, 共 %s\n", fileCount, dirCount, human.Size(totalBytes))
	}
	return nil
}

// printNode 递归打印节点。root 节点自身不打印，只打印其子项。
func printNode(w io.Writer, n *treeNode, prefix string, isRoot bool, maxDepth, depth int, showMod bool) {
	children := sortedChildren(n)

	for i, c := range children {
		last := i == len(children)-1
		connector, childPrefix := "├── ", "│   "
		if last {
			connector, childPrefix = "└── ", "    "
		}
		line := prefix + connector + c.name
		if c.isDir {
			line += "/"
		} else {
			line += fmt.Sprintf("  (%s)", human.Size(c.size))
			if showMod {
				line += fmt.Sprintf("  %s", c.mod.Format("2006-01-02 15:04"))
			}
		}
		fmt.Fprintln(w, line)

		if c.isDir && (maxDepth <= 0 || depth+1 < maxDepth) {
			printNode(w, c, prefix+childPrefix, false, maxDepth, depth+1, showMod)
		}
	}
}

func sortedChildren(n *treeNode) []*treeNode {
	out := make([]*treeNode, 0, len(n.children))
	for _, c := range n.children {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].isDir != out[j].isDir {
			return out[i].isDir // 目录在前
		}
		return out[i].name < out[j].name
	})
	return out
}
