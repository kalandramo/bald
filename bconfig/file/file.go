package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/kalandramo/bald/bconfig"
)

var (
	_ bconfig.Reader       = (*source)(nil)
	_ bconfig.ValueWatcher = (*source)(nil)
	_ bconfig.Closer       = (*source)(nil)
)

type source struct {
	options *options
	watcher *fsnotify.Watcher
}

// New creates a file-based config source.
// The path option is required; an error is returned if it is empty.
func New(opts ...Option) (*source, error) {
	o := &options{
		ctx: context.Background(),
	}
	for _, opt := range opts {
		opt(o)
	}

	if o.path == "" {
		return nil, errors.New("path invalid")
	}

	// Resolve to absolute path so that fsnotify events match.
	abs, err := filepath.Abs(o.path)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute path: %w", err)
	}
	o.path = abs

	s := &source{options: o}

	if o.watch {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, fmt.Errorf("create fsnotify watcher: %w", err)
		}
		s.watcher = w

		// Watch the parent directory — file-level watches are lost on atomic
		// renames (editors often write to a temp file then rename).
		dir := filepath.Dir(o.path)
		if err := w.Add(dir); err != nil {
			_ = w.Close()
			return nil, fmt.Errorf("watch directory %s: %w", dir, err)
		}
	}

	return s, nil
}

// resolveKey returns the key to use for the given caller-provided key.
// If key is empty the configured default path is used.
func (s *source) resolveKey(key string) string {
	if key != "" {
		abs, err := filepath.Abs(key)
		if err != nil {
			return key
		}
		return abs
	}
	return s.options.path
}

// Load implements [baseConfig.Reader].
// It reads the entire file at key (or the configured default path when key
// is empty) and returns its raw contents.
func (s *source) Load(_ context.Context, key string) ([]byte, error) {
	path := s.resolveKey(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file %s: %w", path, err)
	}
	return data, nil
}

// WatchValue implements [baseConfig.ValueWatcher].
// It returns a channel that receives the new file contents each time the file
// at key (or the configured default path) is modified or created.
// The channel is closed when ctx is cancelled or the watcher encounters a
// fatal error.
//
// 派发的是「稳定后的内容」而不是「每一次写」：内容能被连续两次读到一致才派发（空
// 内容再递进确认，见 [readSettled]），所以高频重写期间会合并、写到一半的中间态不
// 派发，而最后一个值一定会派发。消费侧按「收到的总是完整内容」来写即可。
// 残留：写入方在 truncate 与 write 之间停摆超过整个确认窗时，读侧无法与「文件真的
// 被清空」区分，那种情况仍会派发一次空载荷（紧跟着的还是最终值）。
func (s *source) WatchValue(ctx context.Context, key string) (<-chan []byte, error) {
	path := s.resolveKey(key)

	if s.watcher == nil {
		// Lazy-init a watcher if the source was created without watch enabled.
		w, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, fmt.Errorf("create fsnotify watcher: %w", err)
		}
		dir := filepath.Dir(path)
		if err := w.Add(dir); err != nil {
			_ = w.Close()
			return nil, fmt.Errorf("watch directory %s: %w", dir, err)
		}
		s.watcher = w
	}

	out := make(chan []byte, 1)

	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-s.watcher.Events:
				if !ok {
					return
				}
				// Only react to events for our target file.
				if event.Name != path {
					continue
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
					continue
				}
				data, settled, err := readSettled(ctx, path)
				if err != nil {
					// Transient failure (editors often write atomically and briefly
					// remove the target) — skip this event; the next one will re-read.
					continue
				}
				if !settled {
					// Still being rewritten — dispatching now would hand the
					// consumer a half-written snapshot. The event for the last
					// write settles and delivers the final content.
					continue
				}
				select {
				case out <- data:
				case <-ctx.Done():
					return
				}
			case err, ok := <-s.watcher.Errors:
				if !ok {
					return
				}
				// Watcher is fatally broken — terminate the loop so the output
				// channel is closed and the caller observes the watcher has stopped.
				_ = err
				return
			}
		}
	}()

	return out, nil
}

// Close releases the fsnotify watcher.
func (s *source) Close() error {
	if s.watcher != nil {
		return s.watcher.Close()
	}
	return nil
}

// 就地重写（O_TRUNC 后再写）会被观察到「文件是 0 字节」的瞬间，而消费侧
// `bootstrap/config` 的 apply 把空载荷解成「零 key 文档」（`decodeDocument` 对
// `len(data)==0` 返回 `nil, nil`，不是错误），于是这一层缓存被清空并触发一次
// OnChange——如果这是最后一次写，配置会一直缺项到下一次真实变更为止。
//
// 下面两个窗口按实测定的（把 `readSettled` 改成「直接派发第一次读」即可复现：
// linux/arm64 容器 `-race` 下 50 轮 50ms 间隔重写有 14~18 轮派发过空内容）：
// 连续两次一致（5ms 间隔）挡掉「读到一半」，空载荷再多等 50ms×4 复核。空窗之所以
// 要比非空长得多：`-race` + 高频重写能把 O_TRUNC 与 Write 之间的间隔拉到 5ms 以上
// （darwin 实测只判 5ms 一致时仍有 25/28 次派发是空的），共享 CPU 的容器里甚至能
// 拉到 200ms 以上——那种情况下确认窗读满仍是空，只能按「文件真被清空」派发。
const (
	settleInterval      = 5 * time.Millisecond
	settleAttempts      = 4
	emptyConfirmWait    = 50 * time.Millisecond
	emptyConfirmAttempt = 4
)

// readSettled 读文件直到连续两次读到一致的内容，两次读之间隔 [settleInterval]；
// 若一致的结果是空内容，再按 [confirmEmpty] 递进确认。
// settled=false 表示用完 [settleAttempts] 次仍未一致（文件正被连续重写），
// 调用方必须放弃这次派发——读到一半的内容不是任何一个真实的配置状态。
//
// 代价是每次派发最坏多等 settleInterval*settleAttempts；空内容再多等
// emptyConfirmWait*emptyConfirmAttempt。
//
// 这不是「永不派发空内容」的保证：写入方若在 O_TRUNC 与 Write 之间停摆超过整个
// 确认窗，读侧看到的就是一个真实的空文件，无从与「运维清空了配置」区分。要彻底
// 消除中间态得由写入方原子发布（临时文件 + rename），本源 watch 的正是父目录，
// 那种写法不会丢事件。
func readSettled(ctx context.Context, path string) (data []byte, settled bool, err error) {
	first, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	for range settleAttempts {
		timer := time.NewTimer(settleInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false, ctx.Err()
		case <-timer.C:
		}
		next, err := os.ReadFile(path)
		if err != nil {
			return nil, false, err
		}
		if bytes.Equal(first, next) {
			if len(next) > 0 {
				return next, true, nil
			}
			return confirmEmpty(ctx, path)
		}
		first = next
	}
	return first, false, nil
}

// confirmEmpty 处理唯一有歧义的一致读法：连续两次都读到空。空载荷在消费侧等于
// 「这一层没有 key」，所以要连续多个写入窗口都读不到内容才认。被真正清空的配置
// 文件仍然会派发，只是最坏多等 emptyConfirmWait*emptyConfirmAttempt。
func confirmEmpty(ctx context.Context, path string) ([]byte, bool, error) {
	for range emptyConfirmAttempt {
		timer := time.NewTimer(emptyConfirmWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false, ctx.Err()
		case <-timer.C:
		}
		again, err := os.ReadFile(path)
		if err != nil {
			return nil, false, err
		}
		if len(again) > 0 {
			// 空窗口只是写到一半，这一次才是真实内容。
			return again, true, nil
		}
	}
	return nil, true, nil
}
