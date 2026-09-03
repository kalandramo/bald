package bconfig

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// FallbackReader 按照优先级顺序尝试多个 [Reader] 配置源。
//
// 执行 Load 读取时，会按顺序逐个尝试各个配置源；第一个返回非空数据且无错误的源即为结果。
// 该类型实现了级联回退模式：高优先级配置源会覆盖低优先级配置源。
//
// FallbackReader 同时还实现以下接口：
//   - [Closer]       — 关闭所有实现了 [Closer] 的子配置源。
//   - [ValueWatcher] — 合并所有实现 [ValueWatcher] 的子配置源的变更通知。
//
//     任意子配置源推送变更事件时，会通过 Load（遵循优先级规则）重新读取有效配置值，
//     并转发到合并后的输出通道。
type FallbackReader struct {
	readers []Reader
}

// NewFallbackReader 根据传入的多个读取器创建 [FallbackReader]。
// 读取器会按照传入顺序依次尝试；第一个传入的读取器优先级最高。
// 必须至少传入一个读取器实例。
func NewFallbackReader(readers ...Reader) (*FallbackReader, error) {
	if len(readers) == 0 {
		return nil, errors.New("fallback: at least one reader is required")
	}
	return &FallbackReader{readers: readers}, nil
}

// Load 实现 [Reader] 接口。按优先级顺序依次尝试各个子配置源，返回第一个执行成功且返回非空数据的配置源的数据。
// 如果所有配置源均失败或都返回 nil，则返回合并后的错误信息。
func (f *FallbackReader) Load(ctx context.Context, key string) ([]byte, error) {
	var errs []error
	for _, r := range f.readers {
		data, err := r.Load(ctx, key)
		if err == nil && data != nil {
			return data, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, fmt.Errorf("fallback: no source could resolve key %q", key)
}

// Close 实现 [Closer] 接口。关闭所有实现了 [Closer] 的子配置源，返回聚合后的错误；若无错误则返回 nil。
func (f *FallbackReader) Close() error {
	var errs []error
	for _, r := range f.readers {
		if c, ok := r.(Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// WatchValue 实现 [ValueWatcher] 接口。监听所有实现了 [ValueWatcher] 的子配置源，
// 并将它们的变更通知合并到同一个通道。当任意子配置源检测到变更时，
// 会调用 Load 重新读取生效配置值（遵循优先级规则），再将结果转发出去。
//
// 注意：本方法只合并实现了 [ValueWatcher] 的子源（不识别 [Watcher]）。这是刻意选择——
// 不引入适配器层去兼容信号源，转换责任下沉到 provider（见 [Watcher] 注释）：
// 既能避免每次变更多一次无谓的 [Reader.Load]，也消除了「信号源在组合层被静默忽略」的误导。
//
// 当上下文被取消，或是所有子配置源的监听器全部结束时，返回的通道会被关闭。
func (f *FallbackReader) WatchValue(ctx context.Context, key string) (<-chan []byte, error) {
	// 筛选出支持 ValueWatcher 的子配置源。
	var watchers []ValueWatcher
	for _, r := range f.readers {
		if vw, ok := r.(ValueWatcher); ok {
			watchers = append(watchers, vw)
		}
	}
	if len(watchers) == 0 {
		return nil, fmt.Errorf("fallback: none of the sub-sources implement ValueWatcher")
	}

	// 在启动协程之前先收集全部子通道，这样即便某个监听器启动失败，也不会造成协程泄漏。
	var subs []<-chan []byte
	for _, w := range watchers {
		ch, err := w.WatchValue(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("fallback: sub-source WatchValue failed: %w", err)
		}
		if ch != nil {
			subs = append(subs, ch)
		}
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("fallback: all sub-source WatchValue returned nil channels")
	}

	out := make(chan []byte, 1)
	var wg sync.WaitGroup

	for _, ch := range subs {
		wg.Add(1)
		go func(ch <-chan []byte) {
			defer wg.Done()
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						return
					}
					// 重新读取，获取遵循优先级规则的生效配置值。
					effective, err := f.Load(ctx, key)
					if err != nil || effective == nil {
						continue
					}
					select {
					case out <- effective:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}(ch)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out, nil
}

// 编译期接口断言。
var (
	_ Reader       = (*FallbackReader)(nil)
	_ Closer       = (*FallbackReader)(nil)
	_ ReadCloser   = (*FallbackReader)(nil)
	_ ValueWatcher = (*FallbackReader)(nil)
)
