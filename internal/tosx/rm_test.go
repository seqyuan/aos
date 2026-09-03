package tosx

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// fakeOps 构造注入的存储操作，记录调用以便断言。
func fakeOps() (rmOps, *fakeRMState) {
	st := &fakeRMState{}
	return rmOps{
		listObjects: func(_ context.Context, bucket, prefix string) ([]tos.ListedObjectV2, error) {
			return st.listObjects, st.listErr
		},
		deleteOne: func(_ context.Context, bucket, key string) error {
			st.deleted = append(st.deleted, key)
			return st.deleteOneErr
		},
		deleteBatch: func(_ context.Context, bucket string, keys []string) []string {
			st.batchCalls = append(st.batchCalls, append([]string{}, keys...))
			return st.failedKeys
		},
		listUploads: func(_ context.Context, bucket, prefix string) ([]tos.ListedUpload, error) {
			return st.uploads, st.uploadsErr
		},
		abortUpload: func(_ context.Context, bucket, key, uploadID string) error {
			st.aborted = append(st.aborted, key+"#"+uploadID)
			return st.abortErr
		},
	}, st
}

type fakeRMState struct {
	listObjects  []tos.ListedObjectV2
	listErr      error
	deleteOneErr error
	failedKeys   []string
	batchCalls   [][]string
	uploads      []tos.ListedUpload
	uploadsErr   error
	abortErr     error
	aborted      []string
	deleted      []string
}

func TestRMDeleteSingleObject(t *testing.T) {
	ops, st := fakeOps()
	var buf bytes.Buffer
	res, err := rmExecute(context.Background(), ops, "b", "dir/file.txt/", RMOptions{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedObjects != 1 || len(st.deleted) != 1 || st.deleted[0] != "dir/file.txt" {
		t.Fatalf("res=%+v deleted=%v", res, st.deleted)
	}
	if !strings.Contains(buf.String(), "已删除 1 个对象") {
		t.Fatalf("报告缺失: %q", buf.String())
	}
}

func TestRMDeleteSingleObjectNotFoundIsIdempotent(t *testing.T) {
	ops, st := fakeOps()
	st.deleteOneErr = errors.New("tos: NoSuchKey: not found")
	var buf bytes.Buffer
	res, err := rmExecute(context.Background(), ops, "b", "k.txt/", RMOptions{}, &buf)
	if err != nil {
		t.Fatalf("对象不存在应视为删除成功: %v", err)
	}
	if res.DeletedObjects != 1 {
		t.Fatalf("res=%+v", res)
	}
}

func TestRMDeleteSingleObjectRequiresKey(t *testing.T) {
	ops, _ := fakeOps()
	var buf bytes.Buffer
	// prefix 为空（路径只到 bucket）→ 报错提示 key
	if _, err := rmExecute(context.Background(), ops, "b", "", RMOptions{}, &buf); err == nil {
		t.Fatal("空 key 应报错")
	}
}

func TestRMRecursiveConfirmRejected(t *testing.T) {
	ops, st := fakeOps()
	st.listObjects = []tos.ListedObjectV2{{Key: "d/a.txt", Size: 1}, {Key: "d/b.txt", Size: 2}}
	st.uploads = []tos.ListedUpload{{Key: "d/big.bin", UploadID: "u1"}}
	var buf bytes.Buffer
	confirmed := false
	res, err := rmExecute(context.Background(), ops, "b", "d/", RMOptions{
		Recursive: true,
		Confirm: func(prompt string) (bool, error) {
			if !strings.Contains(prompt, "2 个对象") || !strings.Contains(prompt, "1 个未完成分片") {
				t.Fatalf("确认提示不完整: %q", prompt)
			}
			return confirmed, nil
		},
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedObjects != 0 || len(st.batchCalls) != 0 || len(st.aborted) != 0 {
		t.Fatalf("确认拒绝后不应删除: res=%+v batch=%v aborted=%v", res, st.batchCalls, st.aborted)
	}
	if !strings.Contains(buf.String(), "已取消") {
		t.Fatalf("应提示已取消: %q", buf.String())
	}
}

func TestRMRecursiveConfirmAccepted(t *testing.T) {
	ops, st := fakeOps()
	st.listObjects = []tos.ListedObjectV2{{Key: "d/a.txt", Size: 1}, {Key: "d/b.txt", Size: 2}}
	st.uploads = []tos.ListedUpload{{Key: "d/big.bin", UploadID: "u1"}, {Key: "d/big2.bin", UploadID: "u2"}}
	var buf bytes.Buffer
	res, err := rmExecute(context.Background(), ops, "b", "d/", RMOptions{
		Recursive: true,
		Confirm:   func(string) (bool, error) { return true, nil },
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.batchCalls) != 1 || len(st.batchCalls[0]) != 2 {
		t.Fatalf("batch 调用异常: %v", st.batchCalls)
	}
	if res.DeletedObjects != 2 || res.AbortedUploads != 2 {
		t.Fatalf("res=%+v", res)
	}
	if len(st.aborted) != 2 || st.aborted[0] != "d/big.bin#u1" {
		t.Fatalf("abort 调用异常: %v", st.aborted)
	}
	if !strings.Contains(buf.String(), "已删除 2 个对象") || !strings.Contains(buf.String(), "清理 2 个未完成分片") {
		t.Fatalf("报告缺失: %q", buf.String())
	}
}

func TestRMRecursiveForceSkipsConfirm(t *testing.T) {
	ops, st := fakeOps()
	st.listObjects = []tos.ListedObjectV2{{Key: "d/a.txt", Size: 1}}
	confirmCalled := false
	var buf bytes.Buffer
	res, err := rmExecute(context.Background(), ops, "b", "d/", RMOptions{
		Recursive: true,
		Force:     true,
		Confirm: func(string) (bool, error) {
			confirmCalled = true
			return false, nil
		},
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if confirmCalled {
		t.Fatal("-f 不应触发确认")
	}
	if res.DeletedObjects != 1 {
		t.Fatalf("res=%+v", res)
	}
}

func TestRMRecursivePartialFailure(t *testing.T) {
	ops, st := fakeOps()
	st.listObjects = []tos.ListedObjectV2{{Key: "d/a.txt", Size: 1}, {Key: "d/b.txt", Size: 2}}
	st.failedKeys = []string{"d/b.txt"} // b.txt 删除失败
	var buf bytes.Buffer
	res, err := rmExecute(context.Background(), ops, "b", "d/", RMOptions{
		Recursive: true,
		Force:     true,
	}, &buf)
	if err == nil {
		t.Fatal("有失败对象应返回错误")
	}
	if res.DeletedObjects != 1 || res.FailedObjects != 1 {
		t.Fatalf("res=%+v", res)
	}
	if !strings.Contains(buf.String(), "失败 1 个") {
		t.Fatalf("报告应含失败数: %q", buf.String())
	}
}

func TestRMRecursiveEmptyPrefix(t *testing.T) {
	ops, st := fakeOps()
	st.listObjects = []tos.ListedObjectV2{} // 空
	var buf bytes.Buffer
	res, err := rmExecute(context.Background(), ops, "b", "empty/", RMOptions{
		Recursive: true,
		Force:     true,
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedObjects != 0 || len(st.batchCalls) != 0 {
		t.Fatalf("空前缀不应触发删除: res=%+v batch=%v", res, st.batchCalls)
	}
}

func TestRMRecursiveSkipsDirPlaceholder(t *testing.T) {
	ops, st := fakeOps()
	st.listObjects = []tos.ListedObjectV2{{Key: "d/", Size: 0}, {Key: "d/a.txt", Size: 1}}
	var buf bytes.Buffer
	res, err := rmExecute(context.Background(), ops, "b", "d/", RMOptions{
		Recursive: true,
		Force:     true,
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	// 占位对象 d/ 不应出现在删除列表里
	if res.DeletedObjects != 1 {
		t.Fatalf("res=%+v batch=%v", res, st.batchCalls)
	}
}
