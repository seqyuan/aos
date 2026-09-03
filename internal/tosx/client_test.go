package tosx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsCRCError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("tos: crc of entire file mismatch."), true},
		{errors.New("tos: CRC of entire file mismatch."), true},
		{errors.New("tos: some download task failed."), false},
		{nil, false},
		{errors.New("network error"), false},
	}
	for _, c := range cases {
		if got := isCRCError(c.err); got != c.want {
			t.Errorf("isCRCError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestCleanupDownloadResidue(t *testing.T) {
	dir := t.TempDir()
	checkpointDir := filepath.Join(dir, ".aos", "checkpoints")
	if err := os.MkdirAll(checkpointDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "big.bam")

	// 造残留：checkpoint 文件、temp 文件（SDK 精确名 <目标文件>.temp 与旧版带时间戳两种）、无关文件
	checkpoint := filepath.Join(checkpointDir, "big.bam.abc123.download")
	tmp := filepath.Join(dir, "big.bam.temp.123456")
	tmpExact := filepath.Join(dir, "big.bam.temp")                    // SDK TempFileSuffix 实际命名
	otherCp := filepath.Join(checkpointDir, "other.bam.xyz.download") // 其他文件的 checkpoint，不应删
	keep := filepath.Join(dir, "keep.txt")                            // 无关文件，不应删
	for _, f := range []string{checkpoint, tmp, tmpExact, otherCp, keep} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := cleanupDownloadResidue(checkpointDir, dest); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(checkpoint); !os.IsNotExist(err) {
		t.Fatalf("本文件的 checkpoint 应被删除: %v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("本文件的 temp（带时间戳旧命名）应被删除: %v", err)
	}
	if _, err := os.Stat(tmpExact); !os.IsNotExist(err) {
		t.Fatalf("本文件的 temp（SDK 精确命名）应被删除: %v", err)
	}
	if _, err := os.Stat(otherCp); err != nil {
		t.Fatalf("其他文件的 checkpoint 不应被删除: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("无关文件不应被删除: %v", err)
	}
}
